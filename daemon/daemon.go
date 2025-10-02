package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lunixbochs/struc"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"go.etcd.io/bbolt"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/common/systemd"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

const (
	typeImage uint16 = iota
	typeSlabImage
	typeSlab
	typeManifestSlab
)

const (
	schemaV0 uint32 = iota
	schemaV1        // add catalog, shrunk sph in loc bytes

	schemaLatest = schemaV1
)

const (
	// savedFdName = "devnode" // FIXME
	presentMask        = 1 << 31
	reservedBlocks     = 4 // reserved at beginning of slab
	manifestSlabOffset = 10000
)

type (
	Server struct {
		cfg        *Config
		post       atomic.Pointer[postinit]
		blockShift shift.Shift
		db         *bbolt.DB
		msgPool    *sync.Pool
		chunkPool  *common.ChunkPool
		builder    *erofs.Builder
		devnode    atomic.Int32 // FIXME: notify state?
		stats      daemonStats

		stateLock sync.Mutex
		// cacheState   map[uint32]*openFileState // object id -> state
		// stateBySlab  map[uint16]*openFileState // slab id -> state
		// readfdBySlab map[uint16]int            // slab id -> readfd
		// readfds are kept in a separate map because after a restore, we may load the slab
		// image and readfd before the slab is loaded by erofs. this doesn't make
		// sense but it seems to work that way.
		// FIXME: revisit this? we can consolidate...
		// only need manifest slab in here, right?
		slabFds map[uint16]int

		// keeps track of locs that we know are present before we persist them
		presentMap common.SimpleSyncMap[erofs.SlabLoc, struct{}]

		// tracks reads for chunks that we should have, to detect bugs
		readKnownMap common.SimpleSyncMap[erofs.SlabLoc, int]

		// keeps track of pending diff/fetch state
		// note: we open a read-only transaction inside of diffLock.
		// therefore we must not try to lock diffLock while in a read or write tx.
		diffLock    sync.Mutex
		diffMap     map[erofs.SlabLoc]reqOp
		recentReads map[string]*recentRead
		diffSem     *semaphore.Weighted

		remanifestCache common.SimpleSyncMap[string, *remanifestCacheEntry]

		shutdownChan chan struct{}
		shutdownWait sync.WaitGroup
	}

	// fields that are only known after init
	postinit struct {
		keys   []signature.PublicKey
		params pb.DaemonParams
		csread manifester.ChunkStoreRead
		mcread manifester.ChunkStoreRead
	}

	openFileState struct {
		writeFd uint32 // for slabs, slab images, and store images
		tp      uint16

		// for slabs, slab images, and manifest slabs
		slabId uint16
	}

	Config struct {
		CachePath string

		ErofsBlockShift int
		// SmallFileCutoff int

		Workers int

		IsTesting bool
		FdStore   systemd.FdStore
	}
)

var _ erofs.SlabManager = (*Server)(nil)
var errAlreadyMounted = errors.New("already mounted")
var errAlreadyMountedElsewhere = errors.New("already mounted on another mountpoint")

// init stuff

func NewServer(cfg Config) *Server {
	return &Server{
		cfg:             &cfg,
		blockShift:      shift.Shift(cfg.ErofsBlockShift),
		chunkPool:       common.NewChunkPool(),
		builder:         erofs.NewBuilder(erofs.BuilderConfig{BlockShift: cfg.ErofsBlockShift}),
		cacheState:      make(map[uint32]*openFileState),
		stateBySlab:     make(map[uint16]*openFileState),
		readfdBySlab:    make(map[uint16]int),
		presentMap:      *common.NewSimpleSyncMap[erofs.SlabLoc, struct{}](),
		readKnownMap:    *common.NewSimpleSyncMap[erofs.SlabLoc, int](),
		diffMap:         make(map[erofs.SlabLoc]reqOp),
		recentReads:     make(map[string]*recentRead),
		diffSem:         semaphore.NewWeighted(int64(cfg.Workers)),
		remanifestCache: *common.NewSimpleSyncMap[string, *remanifestCacheEntry](),
		shutdownChan:    make(chan struct{}),
	}
}

func (s *Server) p() *postinit {
	return s.post.Load()
}

func (s *Server) postInit(params *pb.DaemonParams, keys []signature.PublicKey) error {
	post := &postinit{
		keys:   keys,
		csread: manifester.NewChunkStoreReadUrl(params.ChunkReadUrl, manifester.ChunkReadPath),
		mcread: manifester.NewChunkStoreReadUrl(params.ManifestCacheUrl, manifester.ManifestCachePath),
	}
	proto.Merge(&post.params, params)
	if !s.post.CompareAndSwap(nil, post) {
		return errors.New("postInit got conflict")
	}
	return nil
}

func (s *Server) openDb() (err error) {
	opts := bbolt.Options{
		NoFreelistSync: true,
		FreelistType:   bbolt.FreelistMapType,
	}

	dbPath := filepath.Join(s.cfg.CachePath, dbFilename)

	if os.Remove(filepath.Join(s.cfg.CachePath, compactFile)) == nil {
		// request to compact db
		ctime := time.Now().UTC().Format(time.RFC3339)
		newPath := dbPath + ".new." + ctime
		cmpPath := dbPath + ".compacted." + ctime
		if newDb, err := bbolt.Open(newPath, 0644, &opts); err == nil {
			if oldDb, err := bbolt.Open(dbPath, 0644, &opts); err == nil {
				if err := bbolt.Compact(newDb, oldDb, 4<<20); err == nil {
					oldDb.Close()
					newDb.Close()
					if os.Rename(dbPath, cmpPath) == nil {
						os.Rename(newPath, dbPath)
						log.Println("compacted db, old file in", cmpPath)
					}
				} else {
					log.Println("bolt compact error:", err)
				}
				oldDb.Close()
			}
			newDb.Close()
		}
	}

	s.db, err = bbolt.Open(dbPath, 0644, &opts)
	if err != nil {
		return err
	}
	s.db.MaxBatchDelay = 100 * time.Millisecond

	checkSchemaVer := func(mb *bbolt.Bucket) error {
		b := mb.Get(metaSchema)
		if len(b) != 4 {
			ver := binary.LittleEndian.AppendUint32(nil, schemaLatest)
			return mb.Put(metaSchema, ver)
		}
		have := binary.LittleEndian.Uint32(b)
		if have != schemaLatest {
			return fmt.Errorf("mismatched schema version %d != %d", have, schemaLatest)
		}
		return nil
	}

	loadParams := func(mb *bbolt.Bucket) error {
		b := mb.Get(metaParams)
		if b == nil {
			// no params yet, leave uninitialized
			log.Print("initializing with empty config, call 'styx init --params=...'")
			return nil
		}
		var dp pb.DbParams
		if err := proto.Unmarshal(b, &dp); err != nil {
			return err
		}
		if err := verifyParams(dp.Params.Params); err != nil {
			return err
		}
		keys, err := common.LoadPubKeys(dp.Pubkey)
		if err != nil {
			return err
		}
		return s.postInit(dp.Params, keys)
	}

	return s.db.Update(func(tx *bbolt.Tx) error {
		if mb, err := tx.CreateBucketIfNotExists(metaBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(chunkBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(slabBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(imageBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(manifestBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(catalogFBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(catalogRBucket); err != nil {
			return err
		} else if err = checkSchemaVer(mb); err != nil {
			return err
		} else if err = loadParams(mb); err != nil {
			return err
		}
		return nil
	})
}

func (s *Server) setupMounts() error {
	// always ensure cache dir exists
	if err := os.MkdirAll(s.cfg.CachePath, 0700); err != nil {
		return err
	}

	// skip all this stuff if we aren't in a private mount ns, most things should still work
	if private, err := havePrivateMountNs(); err != nil || !private {
		return nil
	}

	// remount /nix/store writable so we can manifest in it.
	// ignore failures (maybe it wasn't bind-mounted)
	err := unix.MountSetattr(unix.AT_FDCWD, "/nix/store", 0, &unix.MountAttr{Attr_clr: unix.MOUNT_ATTR_RDONLY})
	if err != nil {
		log.Println("failed to remount /nix/store rw; manifest may not work:", err)
	}

	// we want to make mounts under /var/cache/styx not propagate.
	// to do that, we need to put a mount there (can bind mount it to itself)
	// and set the mount as private.
	// to put a bind mount there and have the bind mount itself not propagate,
	// we need to change the propagation of /, and then change it back.
	// TODO: we can remove this stuff if we're not doing long-term mounts under
	// /var/cache/styx anymore.
	err = unix.MountSetattr(unix.AT_FDCWD, "/", 0, &unix.MountAttr{Propagation: unix.MS_PRIVATE})
	if err != nil {
		log.Println("failed to change propatation on root, skipping cache bind mount:", err)
		return nil
	}

	// restore propagation on /
	defer unix.MountSetattr(unix.AT_FDCWD, "/", 0, &unix.MountAttr{Propagation: unix.MS_SHARED})

	err = unix.Mount(s.cfg.CachePath, s.cfg.CachePath, "none", unix.MS_BIND, "")
	if err != nil {
		log.Println("failed to bind mount cache dir:", err)
		return nil
	}

	err = unix.MountSetattr(unix.AT_FDCWD, s.cfg.CachePath, 0, &unix.MountAttr{Propagation: unix.MS_PRIVATE})
	if err != nil {
		log.Println("failed to cache propatation on cache dir:", err)
		return nil
	}

	return nil
}

// FIXME: consolidate with createSlabFile?
func (s *Server) setupManifestSlab() error {
	var id uint16 = manifestSlabOffset
	path := filepath.Join(s.cfg.CachePath, "slab", strconv.Itoa(int(id)))
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		log.Println("open manifest slab", path, err)
		return err
	}

	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	s.slabFds[id] = fd
	// state := &openFileState{
	// 	writeFd: common.TruncU32(fd), // write and read to same fd
	// 	tp:      typeManifestSlab,
	// 	slabId:  id,
	// }
	// s.stateBySlab[id] = state
	// s.readfdBySlab[id] = fd
	return nil
}

func (s *Server) setupNotify() error {
	log.Println("set up fanotify")
	fd, err := unix.FanotifyInit(
		unix.FAN_CLASS_PRE_CONTENT|
			unix.FAN_CLOEXEC|
			unix.FAN_NONBLOCK|
			unix.FAN_UNLIMITED_QUEUE,
		unix.O_RDWR|
			unix.O_LARGEFILE|
			unix.O_CLOEXEC|
			unix.O_NOATIME,
	)
	if err != nil {
		return fmt.Errorf("fanotifiy_init: %w", err)
	}
	err = unix.FanotifyMark(
		fd,
		unix.FAN_MARK_ADD,
		unix.FAN_PRE_ACCESS,
		unix.AT_FDCWD,
		filepath.Join(s.cfg.CachePath, "slab", strconv.Itoa(0)),
	)
	if err != nil {
		return fmt.Errorf("fanotifiy_mark: %w", err)
	}
	s.devnode.Store(int32(fd))

	// FIXME: TEST
	flags, err := unix.FcntlInt(int(s.devnode.Load()), syscall.F_GETFL, 0)
	if err != nil {
		return err
	}
	log.Println("ISNONBLOCK0", flags&syscall.O_NONBLOCK != 0)

	return nil
}

// socket server + mount management

// Does a transaction on a record in imageBucket. f should mutate its argument and return nil.
// If f returns an error, the record will not be written.
func (s *Server) imageTx(sph string, f func(*pb.DbImage) error) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		var img pb.DbImage
		b := tx.Bucket(imageBucket)
		if buf := b.Get([]byte(sph)); buf != nil {
			if err := proto.Unmarshal(buf, &img); err != nil {
				return err
			}
		}
		if err := f(&img); err != nil {
			return err
		} else if buf, err := proto.Marshal(&img); err != nil {
			return err
		} else {
			return b.Put([]byte(sph), buf)
		}
	})
}

func (s *Server) startSocketServer() (err error) {
	socketPath := filepath.Join(s.cfg.CachePath, Socket)
	os.Remove(socketPath)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socketPath})
	if err != nil {
		return fmt.Errorf("failed to listen on unix socket %s: %w", socketPath, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(InitPath, jsonmw(s.handleInitReq))
	mux.HandleFunc(MountPath, jsonmw(s.handleMountReq))
	mux.HandleFunc(UmountPath, jsonmw(s.handleUmountReq))
	mux.HandleFunc(MaterializePath, jsonmw(s.handleMaterializeReq))
	mux.HandleFunc(VaporizePath, jsonmw(s.handleVaporizeReq))
	mux.HandleFunc(PrefetchPath, jsonmw(s.handlePrefetchReq))
	mux.HandleFunc(GcPath, jsonmw(s.handleGcReq))
	mux.HandleFunc(DebugPath, jsonmw(s.handleDebugReq))
	mux.HandleFunc(RepairPath, jsonmw(s.handleRepairReq))
	mux.HandleFunc("/pprof/", pprof.Index)
	mux.HandleFunc("/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/pprof/profile", pprof.Profile)
	mux.HandleFunc("/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/pprof/trace", pprof.Trace)
	s.shutdownWait.Add(1)
	go func() {
		defer s.shutdownWait.Done()
		srv := &http.Server{Handler: mux}
		go srv.Serve(l)
		<-s.shutdownChan
		log.Printf("stopping http server")
		srv.Close()
	}()
	return nil
}

type errWithStatus struct {
	error
	status int
}

func mwErr(status int, format string, a ...any) error {
	return &errWithStatus{
		error:  fmt.Errorf(format, a...),
		status: status,
	}
}

func mwErrE(status int, e error) error {
	return &errWithStatus{
		error:  e,
		status: status,
	}
}

func jsonmw[reqT, resT any](f func(context.Context, *reqT) (*resT, error)) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if r := recover(); r != nil {
				log.Println("http handler panic", r)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()

		w.Header().Set(common.CTHdr, common.CTJson)
		wEnc := json.NewEncoder(w)

		var req reqT
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			wEnc.Encode(nil)
			return
		}

		parts := make([]any, 0, 7)
		parts = append(parts, r.URL.Path)

		if encReq, err := json.Marshal(req); err == nil {
			parts = append(parts, string(encReq))
		}

		res, err := f(r.Context(), &req)

		if err == nil {
			w.WriteHeader(http.StatusOK)
			if res != nil {
				wEnc.Encode(res)
			} else {
				wEnc.Encode(&Status{Success: true})
			}
			parts = append(parts, " -> ", "OK")
			log.Print(parts...)
			return
		}

		status := http.StatusInternalServerError
		if ewc, ok := err.(*errWithStatus); ok {
			status = ewc.status
		}

		w.WriteHeader(status)
		if res != nil {
			wEnc.Encode(res)
		} else {
			wEnc.Encode(&Status{Success: false, Error: err.Error()})
		}
		parts = append(parts, " -> ", err.Error())
		log.Print(parts...)
	}
}

func (s *Server) handleInitReq(ctx context.Context, r *InitReq) (*Status, error) {
	if s.p() != nil {
		// TODO: add ability to modify some params
		return nil, mwErr(http.StatusConflict, "already initialized")
	} else if err := verifyParams(r.Params.GetParams()); err != nil {
		return nil, mwErrE(http.StatusBadRequest, err)
	} else if len(r.PubKeys) == 0 {
		return nil, mwErr(http.StatusBadRequest, "missing public keys")
	} else if r.Params.ManifesterUrl == "" {
		return nil, mwErr(http.StatusBadRequest, "missing manifester url")
	} else if r.Params.ManifestCacheUrl == "" {
		return nil, mwErr(http.StatusBadRequest, "missing manifest cache url")
	} else if r.Params.ChunkReadUrl == "" {
		return nil, mwErr(http.StatusBadRequest, "missing chunk read url")
	} else if r.Params.ChunkDiffUrl == "" {
		return nil, mwErr(http.StatusBadRequest, "missing chunk diff url")
	} else if keys, err := common.LoadPubKeys(r.PubKeys); err != nil {
		return nil, mwErrE(http.StatusBadRequest, err)
	} else if err = s.postInit(&r.Params, keys); err != nil {
		return nil, err
	}
	return nil, s.db.Update(func(tx *bbolt.Tx) error {
		mb := tx.Bucket(metaBucket)
		if mb.Get(metaParams) != nil {
			// shouldn't happen here since postInit does CAS
			return errors.New("conflict on meta params update")
		}
		dp := pb.DbParams{
			Params: &r.Params,
			Pubkey: r.PubKeys,
		}
		if b, err := proto.Marshal(&dp); err != nil {
			return err
		} else {
			return mb.Put(metaParams, b)
		}
	})
}

func (s *Server) handleMountReq(ctx context.Context, r *MountReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}
	_, sphStr, _, err := ParseSphAndName(r.StorePath)
	if err != nil {
		return nil, err
	} else if r.Upstream == "" {
		return nil, mwErr(http.StatusBadRequest, "invalid upstream")
	} else if !strings.HasPrefix(r.MountPoint, "/") {
		return nil, mwErr(http.StatusBadRequest, "mount point must be absolute path")
	}

	common.NormalizeUpstream(&r.Upstream)

	err = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if img.MountState == pb.MountState_Mounted {
			if img.MountPoint == r.MountPoint {
				// nix thinks it's not mounted but it is. return success so nix can enter in db.
				return errAlreadyMounted
			} else {
				return errAlreadyMountedElsewhere
			}
		}
		img.StorePath = r.StorePath
		img.Upstream = r.Upstream
		img.MountState = pb.MountState_Requested
		img.MountPoint = r.MountPoint
		img.LastMountError = ""
		img.NarSize = r.NarSize
		return nil
	})
	if err != nil {
		if err == errAlreadyMounted {
			err = nil
		}
		return nil, err
	}

	return nil, s.tryMount(ctx, r)
}

func (s *Server) tryMount(ctx context.Context, req *MountReq) error {
	_, sphStr, _ := ParseSph(req.StorePath)

	path := filepath.Join(s.cfg.CachePath, "image", sphStr)

	var imagePrefix []byte
	if f, err := os.Open(path); err == nil {
		// if we have an image we can proceed right to mounting
		imagePrefix, err = io.ReadAll(io.LimitReader(f, 4096))
		if err != nil {
			f.Close()
			return err
		}
		f.Close()
	} else {
		// if no image yet, get the manifest and build it
		_, image, err := s.getManifestAndBuildImage(ctx, req)
		if err != nil {
			return err
		}
		if err = os.WriteFile(path+".tmp", image, 0o644); err != nil {
			os.Remove(path + ".tmp")
			return err
		} else if err = os.Rename(path+".tmp", path); err != nil {
			os.Remove(path + ".tmp")
			return err
		}
		imagePrefix = image[:4096]
	}

	// do real mount
	var mountErr error
	isBare := erofs.IsBare(imagePrefix)
	if isBare {
		// set up empty file on target mount point
		if st, err := os.Lstat(req.MountPoint); err != nil || !st.Mode().IsRegular() {
			if err = os.RemoveAll(req.MountPoint); err != nil {
				return fmt.Errorf("error clearing mount point for bare file: %w", err)
			} else if err = os.WriteFile(req.MountPoint, nil, 0o644); err != nil {
				return fmt.Errorf("error creating mount point for bare file: %w", err)
			}
		}
		// mount to private dir
		privateMp := filepath.Join(s.cfg.CachePath, "bare", sphStr)
		_ = os.MkdirAll(privateMp, 0o755)
		mountErr = unix.Mount(path, privateMp, "erofs", 0, "")
		if mountErr == nil {
			// now bind the bare file where it should go
			mountErr = unix.Mount(privateMp+erofs.BarePath, req.MountPoint, "none", unix.MS_BIND, "")
		}
		// whether we succeeded or failed, unmount the original and clean up
		_ = unix.Unmount(privateMp, 0)
		_ = os.Remove(privateMp)
	} else {
		_ = os.MkdirAll(req.MountPoint, 0o755)
		mountErr = unix.Mount(path, req.MountPoint, "erofs", 0, "")
	}

	_ = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if mountErr == nil {
			img.MountState = pb.MountState_Mounted
			img.LastMountError = ""
		} else {
			img.MountState = pb.MountState_MountError
			img.LastMountError = mountErr.Error()
		}
		return nil
	})

	if mountErr != nil {
		os.Remove(path) // force refetch/rebuild
	}

	return mountErr
}

func (s *Server) handleUmountReq(ctx context.Context, r *UmountReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}

	// allowed to leave out the name part here
	_, sphStr, err := ParseSph(r.StorePath)
	if err != nil {
		return nil, err
	}

	var mp string
	err = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if img.MountState != pb.MountState_Mounted {
			// TODO: check if erofs is actually mounted anyway and unmount
			return mwErr(http.StatusNotFound, "not mounted")
		} else if mp = img.MountPoint; mp == "" {
			return mwErr(http.StatusInternalServerError, "mount point not set")
		}
		img.MountState = pb.MountState_UnmountRequested
		return nil
	})
	if err != nil {
		return nil, err
	}

	umountErr := unix.Unmount(mp, 0)

	if umountErr == nil {
		_ = s.imageTx(sphStr, func(img *pb.DbImage) error {
			img.MountState = pb.MountState_Unmounted
			img.MountPoint = ""
			return nil
		})
	}

	return nil, umountErr
}

func (s *Server) restoreMounts() {
	var toRestore []*pb.DbImage
	_ = s.db.View(func(tx *bbolt.Tx) error {
		cur := tx.Bucket(imageBucket).Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var img pb.DbImage
			if err := proto.Unmarshal(v, &img); err != nil {
				log.Print("unmarshal error iterating images", string(k), err)
				continue
			}
			// TODO: do this better
			// if img.MountState == pb.MountState_MountError {
			// 	log.Println("fixing", img.MountPoint)
			// 	img.MountState = pb.MountState_Mounted
			// 	img.ImageSize = 0
			// 	toRestore = append(toRestore, &img)
			// 	continue
			// }
			if img.MountState == pb.MountState_Mounted {
				toRestore = append(toRestore, &img)
			}
		}
		return nil
	})
	for _, img := range toRestore {
		if mounted, err := isErofsMount(img.MountPoint); err == nil && mounted {
			// log.Print("restoring: ", img.StorePath, " already mounted on ", img.MountPoint)
			continue
		}
		err := s.tryMount(context.Background(), &MountReq{
			StorePath:  img.StorePath,
			MountPoint: img.MountPoint,
			// the image has been written so we don't need upstream/narsize
		})
		if err == nil {
			log.Print("restoring: ", img.StorePath, " restored to ", img.MountPoint)
		} else {
			log.Print("restoring: ", img.StorePath, " error: ", err)
		}
	}
}

// main server

func (s *Server) Start() error {
	if err := s.setupMounts(); err != nil {
		return fmt.Errorf("error setting up mount namespaces: %w", err)
	}
	if err := s.openDb(); err != nil {
		return fmt.Errorf("error setting up database in %s: %w", s.cfg.CachePath, err)
	}
	if err := s.setupManifestSlab(); err != nil {
		return fmt.Errorf("error setting up manifest slab: %w", err)
	}
	// FIXME: maybe we need to create more?
	if err := s.createSlabFile(0); err != nil {
		return fmt.Errorf("error creating slab 0: %w", err)
	}
	if err := s.setupNotify(); err != nil {
		return err
	}
	if err := s.startSocketServer(); err != nil {
		return err
	}
	go s.pruneRecentCaches()
	go s.notifyServer()
	log.Println("notify server ready, using", s.cfg.CachePath)
	s.restoreMounts()
	s.cfg.FdStore.Ready()
	return nil
}

// this is only for tests! the real daemon doesn't clean up, since we can't restore the cache
// state, it dies and lets systemd keep the devnode open.
func (s *Server) Stop(closeDevnode bool) {
	log.Print("stopping daemon...")
	close(s.shutdownChan) // stops the socket server

	// signal to notify server and workers to stop
	// fd := s.devnode.Swap(0) // FIXME
	// wait for workers to stop
	s.shutdownWait.Wait()
	// close fds of open objects
	s.closeAllFds()
	// maybe close devnode too
	if closeDevnode {
		// unix.Close(int(fd)) // FIXME: notify fd?
		// s.cfg.FdStore.RemoveFd(savedFdName) // FIXME
	}

	s.db.Close()

	log.Print("daemon shutdown done")
}

func (s *Server) closeAllFds() {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	for _, state := range s.cacheState {
		var readFd int
		switch state.tp {
		case typeSlab, typeManifestSlab:
			readFd = s.readfdBySlab[state.slabId]
		}
		s.closeState(state, readFd)
	}
}

func (s *Server) notifyServer() {
	s.shutdownWait.Add(1)
	defer s.shutdownWait.Done()

	f := os.NewFile(uintptr(s.devnode.Load()), "<fanotify>")

	// FIXME: TEST
	flags, err := unix.FcntlInt(int(s.devnode.Load()), syscall.F_GETFL, 0)
	if err == nil {
		log.Println("ISNONBLOCK1", flags&syscall.O_NONBLOCK != 0)
	}

	ch := make(chan []byte)

	for range 4 { // TODO: configurable?
		s.shutdownWait.Add(1)
		go func() {
			defer s.shutdownWait.Done()
			for {
				// buf := s.chunkPool.Get(4096)
				// FIXME: use pools here again, need refcount
				buf := make([]byte, 256)
				n, err := f.Read(buf)
				if err != nil {
					log.Println("fanotify reader got err", err)
					return
				}
				for len(buf) >= 24 {
					n := binary.LittleEndian.Uint32(buf)
					ch <- buf[:n]
					buf = buf[n:]
				}
			}
		}()
	}

	for range s.cfg.Workers {
		s.shutdownWait.Add(1)
		go func() {
			defer s.shutdownWait.Done()
			for buf := range ch {
				s.handleMessage(buf)
			}
		}()
	}

	<-s.shutdownChan

	log.Print("stopping workers")
	f.Close()                          // cause all future reads to error
	time.Sleep(100 * time.Millisecond) // FIXME: wait until all "readers" exit
	close(ch)

	// wchan := make(chan []byte)
	// for i := 0; i < s.cfg.Workers; i++ {
	// 	s.shutdownWait.Add(1)
	// 	go func() {
	// 		defer s.shutdownWait.Done()
	// 		for msg := range wchan {
	// 			s.handleMessage(msg)
	// 		}
	// 	}()
	// }
	//
	// fds := make([]unix.PollFd, 1)
	// errors := 0
	// for {
	// 	if errors > 10 {
	// 		// we might be spinning somehow, slow down
	// 		time.Sleep(time.Duration(errors) * time.Millisecond)
	// 	}
	// 	fd := s.devnode.Load() // FIXME
	// 	if fd == 0 {
	// 		break
	// 	}
	// 	fds[0] = unix.PollFd{Fd: fd, Events: unix.POLLIN}
	// 	timeout := 3600 * 1000
	// 	if s.cfg.IsTesting {
	// 		// use smaller timeout since we can't interrupt this poll (even by closing the fd)
	// 		timeout = 500
	// 	}
	// 	n, err := unix.Poll(fds, timeout)
	// 	if err != nil {
	// 		log.Printf("error from poll: %v", err)
	// 		errors++
	// 		continue
	// 	}
	// 	if n != 1 {
	// 		continue
	// 	}
	// 	if fds[0].Revents&unix.POLLNVAL != 0 {
	// 		break
	// 	}
	// 	// read until we get zero
	// 	for {
	// 		buf := s.chunkPool.Get(4096)
	// 		n, err = unix.Read(int(fd), buf)
	// 		if err != nil {
	// 			errors++
	// 			log.Printf("error from read: %v", err)
	// 			break
	// 		}
	// 		readAfterPoll = true
	// 		errors = 0
	// 		wchan <- buf[:n]
	// 	}
	// }
}

func (s *Server) handleMessage(buf []byte) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("panic in handle:", r)
		}
		if retErr != nil {
			log.Println("handle error:", retErr)
		}
	}()

	var r bytes.Reader
	r.Reset(buf)

	// unpack metadata
	var msg fanotify_event_metadata
	if err := struc.UnpackWithOptions(&r, &msg, &_popts); err != nil {
		return err
	}
	log.Printf("DEBUG EVENT %#v", msg)

	if msg.Vers != unix.FANOTIFY_METADATA_VERSION {
		return errors.New("fanotify metadata version mismatch")
	}

	defer func() {
		if msg.Fd >= 0 {
			_ = unix.Close(int(msg.Fd))
		}
	}()

	// unpack extras
	var rng fanotify_event_info_range
	for r.Len() > 0 {
		var infohdr fanotify_event_info_header
		if err := struc.UnpackWithOptions(&r, &infohdr); err != nil {
			return err
		}
		switch infohdr.InfoType {
		case unix.FAN_EVENT_INFO_TYPE_RANGE:
			if infohdr.Len != 24 {
				log.Println("FAN_EVENT_INFO_TYPE_RANGE had wrong length", infohdr.Len)
			} else if err := struc.UnpackWithOptions(&r, &rng); err != nil {
				return err
			}
		default:
			log.Println("unexpected fanotify info", infohdr)
			_, _ = r.Seek(infohdr.Len-4, io.SeekCurrent)
		}
	}

	if rng.Count == 0 {
		return errors.New("fanotify message did not contain range")
	}

	slabId := uint16(0) // FIXME get from message
	return s.handleReadSlab(msg.Fd, slabId, rng.Count, rng.Offset)

	// switch msg.OpCode {
	// case CACHEFILES_OP_OPEN:
	// 	var open cachefiles_open
	// 	if err := struc.Unpack(&r, &open); err != nil {
	// 		return err
	// 	}
	// 	return s.handleOpen(msg.MsgId, msg.ObjectId, open.Fd, open.Flags, open.VolumeKey, open.CookieKey)
	// case CACHEFILES_OP_CLOSE:
	// 	return s.handleClose(msg.MsgId, msg.ObjectId)
	// case CACHEFILES_OP_READ:
	// 	var read cachefiles_read
	// 	if err := struc.Unpack(&r, &read); err != nil {
	// 		return err
	// 	}
	// 	return s.handleRead(msg.MsgId, msg.ObjectId, read.Len, read.Off)
	// default:
	// 	return errors.New("unknown opcode")
	// }
}

// func (s *Server) handleOpen(msgId, objectId, fd, flags uint32, volume, cookie []byte) (retErr error) {
// 	// volume is "erofs,<domain_id>\x00" (domain_id is same as fsid if not specified)
// 	// cookie is "<fsid>"

// 	var cacheSize int64
// 	fsid := string(cookie)

// 	defer func() {
// 		if r := recover(); r != nil {
// 			retErr = fmt.Errorf("panic in open: %v", r)
// 		}
// 		if retErr != nil {
// 			cacheSize = -int64(unix.ENODEV)
// 		}
// 		reply := fmt.Sprintf("copen %d,%d", msgId, cacheSize)
// 		devfd := int(s.devnode.Load())
// 		if devfd == 0 {
// 			log.Println("closed cachefiles fd in middle of open")
// 			return
// 		}
// 		if _, err := unix.Write(devfd, []byte(reply)); err != nil {
// 			log.Println("failed write to devnode", err)
// 		}
// 		if cacheSize < 0 {
// 			unix.Close(int(fd))
// 		}
// 	}()

// 	if string(volume) != "erofs,"+s.cfg.CacheDomain+"\x00" {
// 		return fmt.Errorf("wrong domain %q", volume)
// 	}

// 	// slab or manifest
// 	if idx := strings.TrimPrefix(fsid, slabPrefix); idx != fsid {
// 		log.Println("open slab", idx, "as", objectId)
// 		slabId, err := strconv.Atoi(idx)
// 		if err != nil {
// 			return err
// 		}
// 		cacheSize, retErr = s.handleOpenSlab(msgId, objectId, fd, flags, common.TruncU16(slabId))
// 		return
// 	} else if idx := strings.TrimPrefix(fsid, slabImagePrefix); idx != fsid {
// 		log.Println("open slab image", idx, "as", objectId)
// 		slabId, err := strconv.Atoi(idx)
// 		if err != nil {
// 			return err
// 		}
// 		cacheSize, retErr = s.handleOpenSlabImage(msgId, objectId, fd, flags, common.TruncU16(slabId))
// 		return
// 	} else if len(fsid) == 32 {
// 		log.Println("open image", fsid, "as", objectId)
// 		cacheSize, retErr = s.handleOpenImage(msgId, objectId, fd, flags, fsid)
// 		return
// 	} else {
// 		return fmt.Errorf("bad fsid %q", fsid)
// 	}
// }

// func (s *Server) handleOpenSlab(msgId, objectId, fd, flags uint32, id uint16) (int64, error) {
// 	// record open state
// 	s.stateLock.Lock()
// 	defer s.stateLock.Unlock()
// 	state := &openFileState{
// 		writeFd: fd,
// 		tp:      typeSlab,
// 		slabId:  id,
// 	}
// 	s.cacheState[objectId] = state
// 	s.stateBySlab[id] = state
// 	return slabBytes, nil
// }

// func (s *Server) handleOpenSlabImage(msgId, objectId, fd, flags uint32, id uint16) (int64, error) {
// 	// record open state
// 	s.stateLock.Lock()
// 	defer s.stateLock.Unlock()
// 	state := &openFileState{
// 		writeFd: fd,
// 		tp:      typeSlabImage,
// 		slabId:  id,
// 	}
// 	s.cacheState[objectId] = state
// 	// always one block
// 	return 1 << s.blockShift, nil
// }

// func (s *Server) handleOpenImage(msgId, objectId, fd, flags uint32, cookie string) (int64, error) {
// 	ctx, _ := s.mountCtxMap.Get(cookie)
// 	if ctx == nil {
// 		return 0, fmt.Errorf("missing context in handleOpenImage for %s", cookie)
// 	}
// 	mountCtx, _ := fromMountCtx(ctx)
// 	if mountCtx == nil {
// 		return 0, fmt.Errorf("missing context in handleOpenImage for %s", cookie)
// 	}

// 	s.stateLock.Lock()
// 	defer s.stateLock.Unlock()
// 	state := &openFileState{
// 		writeFd:   fd,
// 		tp:        typeImage,
// 		imageData: mountCtx.imageData,
// 	}
// 	s.cacheState[objectId] = state
// 	return mountCtx.imageSize, nil
// }

// func (s *Server) handleClose(msgId, objectId uint32) error {
// 	log.Println("close", objectId)
// 	s.stateLock.Lock()
// 	state := s.cacheState[objectId]
// 	if state == nil {
// 		s.stateLock.Unlock()
// 		log.Println("missing state for close")
// 		return nil
// 	}
// 	if state.tp == typeSlab {
// 		delete(s.stateBySlab, state.slabId)
// 	}
// 	var readFd int
// 	switch state.tp {
// 	case typeSlab, typeManifestSlab:
// 		readFd = s.readfdBySlab[state.slabId]
// 		delete(s.readfdBySlab, state.slabId)
// 	}
// 	delete(s.cacheState, objectId)
// 	s.stateLock.Unlock()

// 	// do rest of cleanup outside lock
// 	s.closeState(state, readFd)
// 	return nil
// }

func (s *Server) closeState(state *openFileState, readFd int) {
	fds := []int{int(state.writeFd), readFd}
	slices.Sort(fds)
	fds = slices.Compact(fds)
	if fds[0] == 0 {
		fds = fds[1:]
	}
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
	if state.tp == typeSlab {
		mp := filepath.Join(s.cfg.CachePath, slabImagePrefix+strconv.Itoa(int(state.slabId)))
		_ = unix.Unmount(mp, 0)
	}
}

// func (s *Server) handleRead(msgId, objectId uint32, ln, off uint64) (retErr error) {
// 	s.stateLock.Lock()
// 	state := s.cacheState[objectId]
// 	s.stateLock.Unlock()

// 	if state == nil {
// 		panic("missing state")
// 	}

// 	defer func() {
// 		_, _, e1 := unix.Syscall(unix.SYS_IOCTL, uintptr(state.writeFd), CACHEFILES_IOC_READ_COMPLETE, uintptr(msgId))
// 		if e1 != 0 && retErr == nil {
// 			retErr = fmt.Errorf("ioctl error %d", e1)
// 		}
// 	}()

// 	switch state.tp {
// 	case typeImage:
// 		// log.Printf("read image %5d: %2dk @ %#x", objectId, ln>>10, off)
// 		return s.handleReadImage(state, ln, off)
// 	case typeSlabImage:
// 		// log.Printf("read slab image %5d: %2dk @ %#x", objectId, ln>>10, off)
// 		return s.handleReadSlabImage(state, ln, off)
// 	case typeSlab:
// 		// log.Printf("read slab %5d: %2dk @ %#x", objectId, ln>>10, off)
// 		return s.handleReadSlab(state, ln, off)
// 	default:
// 		panic("bad state type")
// 	}
// }

// func (s *Server) handleReadImage(state *openFileState, _, _ uint64) error {
// 	if state.imageData == nil {
// 		return errors.New("got read request when already written image")
// 	}
// 	// always write whole thing
// 	// TODO: does this have to be page-aligned?
// 	_, err := unix.Pwrite(int(state.writeFd), state.imageData, 0)
// 	if err != nil {
// 		return err
// 	}
// 	state.imageData = nil
// 	return nil
// }

// func (s *Server) handleReadSlabImage(state *openFileState, ln, off uint64) error {
// 	var devid string
// 	if off == 0 {
// 		// only superblock needs this
// 		devid = slabPrefix + strconv.Itoa(int(state.slabId))
// 	}
// 	buf := s.chunkPool.Get(int(ln))
// 	defer s.chunkPool.Put(buf)

// 	b := buf[:ln]
// 	erofs.SlabImageRead(devid, slabBytes, s.blockShift, off, b)
// 	_, err := unix.Pwrite(int(state.writeFd), b, int64(off))
// 	return err
// }

func (s *Server) handleReadSlab(destFd int, slabId uint16, ln, off uint64) (retErr error) {
	s.stats.slabReads.Add(1)
	defer func() {
		if retErr != nil {
			s.stats.slabReadErrs.Add(1)
		}
	}()

	if ln > uint64(shift.MaxChunkShift.Size()) {
		return errors.New("got too big slab read")
	}

	var addr uint32
	var digest cdig.CDig
	var sphps []SphPrefix

	err := s.db.View(func(tx *bbolt.Tx) error {
		sb := tx.Bucket(slabBucket).Bucket(slabKey(slabId))
		if sb == nil {
			return errors.New("missing slab bucket")
		}
		cur := sb.Cursor()
		target := addrKey(common.TruncU32(off >> s.blockShift))
		k, v := cur.Seek(target)
		if k == nil {
			k, v = cur.Last()
		} else if !bytes.Equal(target, k) {
			k, v = cur.Prev()
		}
		if k == nil {
			return errors.New("ran off start of bucket")
		} else if len(v) < cdig.Bytes {
			return errors.New("bad value in loc entry")
		}
		// take addr from key so we write at the right place even if read was in the middle of a chunk
		addr = addrFromKey(k)
		digest = cdig.FromBytes(v)

		// find next to check size. this will be too lenient if we gc'd the chunk right after this,
		// but it's just a sanity check.
		var nextAddr uint32
		if nextK, _ := cur.Next(); nextK == nil {
			nextAddr = common.TruncU32(sb.Sequence())
		} else if nextAddr = addrFromKey(nextK); nextAddr&presentMask != 0 {
			nextAddr = common.TruncU32(sb.Sequence())
		}
		if ln > uint64(nextAddr-addr)<<s.blockShift {
			return errors.New("got too big slab read")
		}

		// look up digest to get store paths
		loc := tx.Bucket(chunkBucket).Get(v)
		if loc == nil {
			return errors.New("missing digest->loc reference")
		}
		sphps = sphpsFromLoc(loc)
		return nil
	})
	if err != nil {
		return err
	}

	if len(sphps) == 0 {
		log.Println("missing sph references for", slabId, addr, digest.String())
	}

	ctx := context.Background()
	return s.requestChunk(ctx, destFd, erofs.SlabLoc{slabId, addr}, digest, sphps)
}

// FIXME: consolidate with setupManifestSlab
func (s *Server) createSlabFile(slabId uint16) error {
	path := filepath.Join(s.cfg.CachePath, "slab", strconv.Itoa(int(slabId)))
	slabFd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return fmt.Errorf("error opening slab file %s: %w", path, err)
	}
	_ = unix.Close(slabFd)

	log.Println("created slab file", slabId)
	return nil
}

// slab manager

const (
	slabBytes = 1 << 40
)

func slabKey(id uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, id)
	return b
}

func addrKey(addr uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, addr)
	return b
}

func addrFromKey(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}

func locValue(id uint16, addr uint32, sph Sph) []byte {
	loc := make([]byte, 6+sphPrefixBytes)
	binary.LittleEndian.PutUint16(loc, id)
	binary.LittleEndian.PutUint32(loc[2:], addr)
	copy(loc[6:], sph[:sphPrefixBytes])
	return loc
}

func loadLoc(b []byte) erofs.SlabLoc {
	return erofs.SlabLoc{binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[2:])}
}

func appendSph(loc []byte, sph Sph) []byte {
	sphPrefix := sph[:sphPrefixBytes]
	sphs := loc[6:]
	for len(sphs) >= sphPrefixBytes {
		if bytes.Equal(sphs[:sphPrefixBytes], sphPrefix) {
			return nil
		}
		sphs = sphs[sphPrefixBytes:]
	}
	newLoc := make([]byte, len(loc)+sphPrefixBytes)
	copy(newLoc, loc)
	copy(newLoc[len(loc):], sphPrefix)
	return newLoc
}

func (s *Server) VerifyParams(blockShift shift.Shift) error {
	if blockShift != s.blockShift {
		return errors.New("mismatched params")
	}
	return nil
}

func (s *Server) AllocateBatch(ctx context.Context, blocks []uint16, digests []cdig.CDig) ([]erofs.SlabLoc, error) {
	sph, forManifest, ok := fromAllocateCtx(ctx)
	if !ok {
		return nil, errors.New("missing allocate context")
	}

	n := len(blocks)
	if n != len(digests) {
		return nil, errors.New("mismatched lengths")
	}
	out := make([]erofs.SlabLoc, n)
	err := s.db.Update(func(tx *bbolt.Tx) error {
		cb, slabroot := tx.Bucket(chunkBucket), tx.Bucket(slabBucket)
		var slabId uint16 = 0
		if forManifest {
			slabId = manifestSlabOffset
		}
		sb, err := slabroot.CreateBucketIfNotExists(slabKey(slabId))
		if err != nil {
			return err
		}
		// reserve some blocks for future purposes
		seq := max(sb.Sequence(), reservedBlocks)

		for i := range out {
			digest := digests[i][:]
			if loc := cb.Get(digest); loc == nil {
				// allocate
				if seq >= slabBytes>>s.blockShift {
					slabId++
					if sb, err = slabroot.CreateBucketIfNotExists(slabKey(slabId)); err != nil {
						return err
					}
					seq = max(sb.Sequence(), reservedBlocks)
				}
				addr := common.TruncU32(seq)
				seq += uint64(blocks[i])
				if err := cb.Put(digest, locValue(slabId, addr, sph)); err != nil {
					return err
				} else if err = sb.Put(addrKey(addr), digest); err != nil {
					return err
				}
				out[i] = erofs.SlabLoc{slabId, addr}
			} else {
				if newLoc := appendSph(loc, sph); newLoc != nil {
					if err := cb.Put(digest, newLoc); err != nil {
						return err
					}
				}
				out[i] = loadLoc(loc)
			}
		}

		return sb.SetSequence(seq)
	})
	return common.ValOrErr(out, err)
}

func (s *Server) SlabInfo(slabId uint16) (tag string, totalBlocks uint32) {
	return slabPrefix + strconv.Itoa(int(slabId)), common.TruncU32(uint64(slabBytes) >> s.blockShift)
}

// like AllocateBatch but only lookup
func (s *Server) lookupLocs(tx *bbolt.Tx, digests []cdig.CDig) ([]erofs.SlabLoc, error) {
	out := make([]erofs.SlabLoc, len(digests))
	cb := tx.Bucket(chunkBucket)
	for i := range out {
		loc := cb.Get(digests[i][:])
		if loc == nil {
			return nil, fmt.Errorf("missing chunk %s in lookupLocs", digests[i])
		}
		out[i] = loadLoc(loc)
	}
	return out, nil
}

var _popts = struc.Options{Order: binary.LittleEndian}
