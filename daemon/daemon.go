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
	"time"

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
	savedFdName        = "nbdsock"
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
		nbdsock    atomic.Value // instance of net.Listener
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
		CachePath  string
		PublicSock string

		ErofsBlockShift int
		// SmallFileCutoff int

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
		cfg:        &cfg,
		blockShift: shift.Shift(cfg.ErofsBlockShift),
		chunkPool:  common.NewChunkPool(),
		builder:    erofs.NewBuilder(erofs.BuilderConfig{BlockShift: cfg.ErofsBlockShift}),
		// cacheState:      make(map[uint32]*openFileState),
		// stateBySlab:     make(map[uint16]*openFileState),
		// readfdBySlab:    make(map[uint16]int),
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
		} else if _, err = tx.CreateBucketIfNotExists(fakeCacheBucket); err != nil {
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

func (s *Server) startSocketServer() error {
	mux := http.NewServeMux()
	mux.HandleFunc(InitPath, jsonmw(s.handleInitReq))
	mux.HandleFunc(MountPath, jsonmw(s.handleMountReq))
	mux.HandleFunc(UmountPath, jsonmw(s.handleUmountReq))
	mux.HandleFunc(MaterializePath, jsonmw(s.handleMaterializeReq))
	mux.HandleFunc(VaporizePath, jsonmw(s.handleVaporizeReq))
	mux.HandleFunc(PrefetchPath, jsonmw(s.handlePrefetchReq))
	mux.HandleFunc(TarballPath, jsonmw(s.handleTarballReq))
	mux.HandleFunc(GcPath, jsonmw(s.handleGcReq))
	mux.HandleFunc(DebugPath, jsonmw(s.handleDebugReq))
	mux.HandleFunc(RepairPath, jsonmw(s.handleRepairReq))
	mux.HandleFunc("/pprof/", pprof.Index)
	mux.HandleFunc("/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/pprof/profile", pprof.Profile)
	mux.HandleFunc("/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/pprof/trace", pprof.Trace)
	err := s.runSocketServer(filepath.Join(s.cfg.CachePath, Socket), mux)
	if err != nil {
		return err
	}

	if s.cfg.PublicSock != "" {
		mux := http.NewServeMux()
		mux.HandleFunc(TarballPath, jsonmw(s.handleTarballReq))
		mux.HandleFunc(DebugPath, jsonmw(s.handleDebugReq))
		err := s.runSocketServer(s.cfg.PublicSock, mux)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) runSocketServer(socketPath string, mux http.Handler) error {
	os.Remove(socketPath)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socketPath})
	if err != nil {
		return fmt.Errorf("failed to listen on unix socket %s: %w", socketPath, err)
	}
	_ = os.Chmod(socketPath, 0o777)
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
		// } else if !s.ondemand() { // FIXME
		// 	return nil, mwErr(http.StatusPreconditionFailed, "styx on-demand features disabled")
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
		// } else if !s.ondemand() { // FIXME
		// 	return nil, mwErr(http.StatusPreconditionFailed, "styx on-demand features disabled")
	}

	// allowed to leave out the name part here
	_, sphStr, err := ParseSph(r.StorePath)
	if err != nil {
		return nil, err
	}

	var mp string
	err = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if img.MountState != pb.MountState_Mounted && img.MountState != pb.MountState_UnmountRequested {
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

	umountErr := unix.Unmount(mp, unix.MNT_DETACH)

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
		return fmt.Errorf("error creating slab file %d: %w", 0, err)
	}
	if err := s.setupNbdSock(); err != nil {
		return fmt.Errorf("listen nbd: %w", 0, err)
	}
	if err := s.startSocketServer(); err != nil {
		return err
	}
	if err := s.startFakeCacheServer(); err != nil {
		return err
	}
	go s.pruneRecentCaches()
	// if ondemand {
	go s.nbdServer()
	// TODO: get number of slabs from db and mount them all
	if err := s.mountSlabImage(0); err != nil {
		log.Print(err)
		// don't exit here, we can operate, just without diffing
	}
	log.Println("nbd server ready")
	s.restoreMounts()
	// } else {
	// 	if err := s.setupFakeSlabImage(0); err != nil {
	// 		log.Print(err)
	// 		// don't exit here, we can operate, just without diffing
	// 	}
	// 	log.Printf("set up slab %d for non-on-demand mode", 0)
	// }
	s.restoreMounts()
	s.cfg.FdStore.Ready()
	return nil
}

// this is only for tests! the real daemon doesn't clean up, since we can't restore the cache
// state, it dies and lets systemd keep the nbd socket open.
func (s *Server) Stop(closeSock bool) {
	log.Print("stopping daemon...")
	close(s.shutdownChan) // stops the socket server

	// signal to notify server and workers to stop
	// fd := s.devnode.Swap(0) // FIXME
	// wait for workers to stop
	s.shutdownWait.Wait()
	// close fds of open objects
	s.closeAllFds()
	// maybe close devnode too
	if closeSock {
		// unix.Close(int(fd)) // FIXME: notify fd?
		s.cfg.FdStore.RemoveFd(savedFdName) // FIXME
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

func (s *Server) handleReadSlab(destFd int, slabId uint16, ln, off uint64) (retErr error) {
	s.stats.slabReads.Add(1)
	defer func() {
		if retErr != nil {
			s.stats.slabReadErrs.Add(1)
		}
	}()

	if ln > uint64(shift.MaxChunkShift.Size()) {
		return fmt.Errorf("got too big slab read @ %d (%d > %d)", off, ln, shift.MaxChunkShift.Size())
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
		nextAddrSrc := "next"
		if nextK, _ := cur.Next(); nextK == nil {
			nextAddr = common.TruncU32(sb.Sequence())
			nextAddrSrc = "end-of-slab-n"
		} else if nextAddr = addrFromKey(nextK); nextAddr&presentMask != 0 {
			nextAddr = common.TruncU32(sb.Sequence())
			nextAddrSrc = "end-of-slab-p"
		}
		chunkEnd := uint64(nextAddr) << s.blockShift
		if off+ln > chunkEnd {
			return fmt.Errorf("got too big slab read @ %d (len %d) past chunk end %d (%s)", off, ln, chunkEnd, nextAddrSrc)
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

// FIXME
// func (s *Server) setupFakeSlabImage(slabId uint16) error {
// 	// If we're not in on-demand mode, set up a plain file in the same place where cachefiles
// 	// would have put it, so that we can get fds to use. Also if this system does switch to
// 	// cachefiles later, it should just work from there.
// 	tag, totalBlocks := s.SlabInfo(slabId)
// 	backingPath := filepath.Join(s.cfg.CachePath, fscachePath(s.cfg.CacheDomain, tag))
// 	_ = os.MkdirAll(filepath.Dir(backingPath), 0o755)
// 	fd, err := unix.Open(backingPath, unix.O_RDWR|unix.O_CREAT, 0o600)
// 	if err != nil {
// 		return err
// 	}
// 	// this doesn't really matter, it might only matter for a transition to cachefiles
// 	_ = unix.Ftruncate(fd, int64(totalBlocks)<<s.blockShift)

// 	s.stateLock.Lock()
// 	defer s.stateLock.Unlock()
// 	s.stateBySlab[slabId] = &openFileState{
// 		writeFd: common.TruncU32(fd), // write and read to same fd
// 		tp:      typeSlab,
// 		slabId:  slabId,
// 	}
// 	s.readfdBySlab[slabId] = slabFds{fd, fd}

// 	return nil
// }
