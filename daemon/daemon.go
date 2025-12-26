package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
	savedFdName = "devnode"
	// preCreatedSlabs    = 1
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
		devnode    atomic.Int32
		stats      daemonStats

		stateLock    sync.Mutex
		cacheState   map[uint32]*openFileState // object id -> state
		stateBySlab  map[uint16]*openFileState // slab id -> state
		readfdBySlab map[uint16]slabFds        // slab id -> readfd, cachefd
		// readfds are kept in a separate map because after a restore, we may load the slab
		// image and readfd before the slab is loaded by erofs/cachefiles. this doesn't make
		// sense but it seems to work that way.

		// keeps track of locs that we know are present before we persist them
		presentMap common.SimpleSyncMap[erofs.SlabLoc, struct{}]

		// tracks reads for chunks that we should have, to detect bugs
		readKnownMap common.SimpleSyncMap[erofs.SlabLoc, int]

		// connect context for mount request to cachefiles request
		mountCtxMap common.SimpleSyncMap[string, context.Context]

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

		// for store images
		imageData []byte // data from manifester to be written
	}

	slabFds struct {
		readFd, cacheFd int
	}

	Config struct {
		DevPath     string
		CachePath   string
		CacheTag    string
		CacheDomain string
		PublicSock  string

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
		msgPool:         &sync.Pool{New: func() any { return make([]byte, CACHEFILES_MSG_MAX_SIZE) }},
		chunkPool:       common.NewChunkPool(),
		builder:         erofs.NewBuilder(erofs.BuilderConfig{BlockShift: cfg.ErofsBlockShift}),
		cacheState:      make(map[uint32]*openFileState),
		stateBySlab:     make(map[uint16]*openFileState),
		readfdBySlab:    make(map[uint16]slabFds),
		presentMap:      *common.NewSimpleSyncMap[erofs.SlabLoc, struct{}](),
		readKnownMap:    *common.NewSimpleSyncMap[erofs.SlabLoc, int](),
		mountCtxMap:     *common.NewSimpleSyncMap[string, context.Context](),
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

func (s *Server) ondemand() bool {
	return s.devnode.Load() > 0
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

func (s *Server) setupManifestSlab() error {
	var id uint16 = manifestSlabOffset
	mfSlabPath := filepath.Join(s.cfg.CachePath, manifestSlabPrefix+strconv.Itoa(int(id)))
	fd, err := unix.Open(mfSlabPath, unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		log.Println("open manifest slab", mfSlabPath, err)
		return err
	}

	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	state := &openFileState{
		writeFd: common.TruncU32(fd), // write and read to same fd
		tp:      typeManifestSlab,
		slabId:  id,
	}
	s.stateBySlab[id] = state
	s.readfdBySlab[id] = slabFds{fd, fd}
	return nil
}

func (s *Server) openDevNode() (int, error) {
	// udev should have created this when the cachefiles module was loaded
	fd, err := unix.Open(s.cfg.DevPath, unix.O_RDWR, 0600)
	if err != nil {
		return 0, fmt.Errorf("error opening %s: %w  (is the cachefiles module loaded?)", s.cfg.DevPath, err)
	} else if _, err = unix.Write(fd, []byte("dir "+s.cfg.CachePath)); err != nil {
		unix.Close(fd)
		return 0, fmt.Errorf("error setting up cachefiles: %w", err)
	} else if _, err = unix.Write(fd, []byte("tag "+s.cfg.CacheTag)); err != nil {
		unix.Close(fd)
		return 0, fmt.Errorf("error setting up cachefiles: %w", err)
	} else if _, err = unix.Write(fd, []byte("bind ondemand")); err != nil {
		unix.Close(fd)
		return 0, fmt.Errorf("error setting up cachefiles ondemand: %w  (is the kernel built with CACHEFILES_ONDEMAND?)", err)
	}
	return fd, nil
}

func (s *Server) setupDevNode() error {
	fd, err := s.cfg.FdStore.GetFd(savedFdName)
	if err == nil {
		if _, err = unix.Write(fd, []byte("restore")); err != nil {
			s.cfg.FdStore.RemoveFd(savedFdName)
			unix.Close(fd)
			// instead of trying to recover, just let systemd restart the process
			// and next time we won't have a saved fd.
			return fmt.Errorf("cachefiles 'restore' failed: %w", err)
		}
		s.devnode.Store(int32(fd))
		log.Println("restored cachefiles device")
		return nil
	}

	fd, err = s.openDevNode()
	if err != nil {
		return err
	}
	s.devnode.Store(int32(fd))
	s.cfg.FdStore.SaveFd(savedFdName, fd)
	log.Println("set up cachefiles device")
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
	} else if !s.ondemand() {
		return nil, mwErr(http.StatusPreconditionFailed, "styx on-demand features disabled")
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

	var haveImageSize int64
	var haveIsBare bool
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
		haveImageSize = img.ImageSize
		haveIsBare = img.IsBare
		return nil
	})
	if err != nil {
		if err == errAlreadyMounted {
			err = nil
		}
		return nil, err
	}

	return nil, s.tryMount(ctx, r, haveImageSize, haveIsBare)
}

func (s *Server) tryMount(ctx context.Context, req *MountReq, haveImageSize int64, haveIsBare bool) error {
	_, sphStr, _ := ParseSph(req.StorePath)

	mountCtx := &mountContext{}
	ctx = withMountContext(ctx, mountCtx)

	if _, ok := s.mountCtxMap.GetOrPut(sphStr, ctx); ok {
		return errors.New("another mount is in progress for this store path")
	}
	defer s.mountCtxMap.Delete(sphStr)

	if haveImageSize > 0 {
		// if we have an image we can proceed right to mounting
		mountCtx.imageSize = haveImageSize
		mountCtx.isBare = haveIsBare
	} else {
		// if no image yet, get the manifest and build it
		_, image, err := s.getManifestAndBuildImage(ctx, req)
		if err != nil {
			return err
		}
		mountCtx.imageSize = int64(len(image))
		mountCtx.isBare = erofs.IsBare(image)
		mountCtx.imageData = image
	}

	var mountErr error
	opts := fmt.Sprintf("domain_id=%s,fsid=%s", s.cfg.CacheDomain, sphStr)

	if mountCtx.imageData != nil {
		// first mount somewhere private, then unmount to force cachefiles to flush the image to disk.
		// this is gross, there should be a better way to control cachefiles flushing.
		firstMp := filepath.Join(s.cfg.CachePath, "initial", sphStr)
		_ = os.MkdirAll(firstMp, 0o755)
		mountErr = unix.Mount("none", firstMp, "erofs", 0, opts)
		_ = unix.Unmount(firstMp, 0)
		_ = os.Remove(firstMp)
	}

	if mountErr == nil {
		// now do real mount
		if mountCtx.isBare {
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
			mountErr = unix.Mount("none", privateMp, "erofs", 0, opts)
			if mountErr == nil {
				// now bind the bare file where it should go
				mountErr = unix.Mount(privateMp+erofs.BarePath, req.MountPoint, "none", unix.MS_BIND, "")
			}
			// whether we succeeded or failed, unmount the original and clean up
			_ = unix.Unmount(privateMp, 0)
			_ = os.Remove(privateMp)
		} else {
			_ = os.MkdirAll(req.MountPoint, 0o755)
			mountErr = unix.Mount("none", req.MountPoint, "erofs", 0, opts)
		}
	}

	_ = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if mountErr == nil {
			img.MountState = pb.MountState_Mounted
			img.LastMountError = ""
			// if the mount succeeded then we must have written the image.
			// record size here so we skip it next time.
			img.ImageSize = mountCtx.imageSize
			img.IsBare = mountCtx.isBare
		} else {
			img.MountState = pb.MountState_MountError
			img.LastMountError = mountErr.Error()
			img.ImageSize = 0 // force refetch/rebuild
		}
		return nil
	})

	return mountErr
}

func (s *Server) handleUmountReq(ctx context.Context, r *UmountReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	} else if !s.ondemand() {
		return nil, mwErr(http.StatusPreconditionFailed, "styx on-demand features disabled")
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
				if img.ImageSize == 0 {
					log.Print("found mounted image without size", img.StorePath)
					continue
				}
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
		}, img.ImageSize, img.IsBare)
		if err == nil {
			log.Print("restoring: ", img.StorePath, " restored to ", img.MountPoint)
		} else {
			log.Print("restoring: ", img.StorePath, " error: ", err)
		}
	}
}

// cachefiles server

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
	if err := s.setupDevNode(); err != nil {
		log.Println("on-demand features disabled:", err)
		// don't abort, support manifest only
	}
	if err := s.startSocketServer(); err != nil {
		return err
	}
	if err := s.startFakeCacheServer(); err != nil {
		return err
	}
	go s.pruneRecentCaches()
	if s.ondemand() {
		go s.cachefilesServer()
		// TODO: get number of slabs from db and mount them all
		if err := s.mountSlabImage(0); err != nil {
			log.Print(err)
			// don't exit here, we can operate, just without diffing
		}
		log.Println("cachefiles server ready, using", s.cfg.CachePath)
		s.restoreMounts()
	} else {
		if err := s.setupFakeSlabImage(0); err != nil {
			log.Print(err)
			// don't exit here, we can operate, just without diffing
		}
		log.Printf("set up slab %d for non-on-demand mode", 0)
	}
	s.cfg.FdStore.Ready()
	return nil
}

// this is only for tests! the real daemon doesn't clean up, since we can't restore the cache
// state, it dies and lets systemd keep the devnode open.
func (s *Server) Stop(closeDevnode bool) {
	log.Print("stopping daemon...")
	close(s.shutdownChan) // stops the socket server

	// signal to cachefiles server and workers to stop
	fd := s.devnode.Swap(0)
	// wait for workers to stop
	s.shutdownWait.Wait()
	// close fds of open objects
	s.closeAllFds()
	// maybe close devnode too
	if closeDevnode {
		unix.Close(int(fd))
		s.cfg.FdStore.RemoveFd(savedFdName)
	}

	s.db.Close()

	log.Print("daemon shutdown done")
}

func (s *Server) closeAllFds() {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	for _, state := range s.cacheState {
		var fds slabFds
		switch state.tp {
		case typeSlab, typeManifestSlab:
			fds = s.readfdBySlab[state.slabId]
		}
		s.closeState(state, fds)
	}
}

func (s *Server) cachefilesServer() {
	s.shutdownWait.Add(1)
	defer s.shutdownWait.Done()

	wchan := make(chan []byte)
	for i := 0; i < s.cfg.Workers; i++ {
		s.shutdownWait.Add(1)
		go func() {
			defer s.shutdownWait.Done()
			for msg := range wchan {
				s.handleMessage(msg)
			}
		}()
	}

	fds := make([]unix.PollFd, 1)
	errors := 0
	for {
		if errors > 10 {
			// we might be spinning somehow, slow down
			time.Sleep(time.Duration(errors) * time.Millisecond)
		}
		fd := s.devnode.Load()
		if fd == 0 {
			break
		}
		fds[0] = unix.PollFd{Fd: fd, Events: unix.POLLIN}
		timeout := 3600 * 1000
		if s.cfg.IsTesting {
			// use smaller timeout since we can't interrupt this poll (even by closing the fd)
			timeout = 500
		}
		n, err := unix.Poll(fds, timeout)
		if err != nil {
			log.Printf("error from poll: %v", err)
			errors++
			continue
		}
		if n != 1 {
			continue
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			break
		}
		// read until we get zero
		readAfterPoll := false
		for {
			buf := s.msgPool.Get().([]byte)
			n, err = unix.Read(int(fd), buf)
			if err != nil {
				errors++
				log.Printf("error from read: %v", err)
				break
			} else if n == 0 {
				// handle bug in linux < 6.8 where poll returns POLLIN if there are any
				// outstanding requests, not just new ones
				if !readAfterPoll {
					log.Printf("empty read from cachefiles device")
					errors++
				}
				break
			}
			readAfterPoll = true
			errors = 0
			wchan <- buf[:n]
		}
	}

	// log.Print("stopping workers")
	close(wchan)
}

func (s *Server) handleMessage(buf []byte) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic in handle: %v", r)
		}
		if retErr != nil {
			log.Printf("error handling message: %v", retErr)
		}
		s.msgPool.Put(buf[0:CACHEFILES_MSG_MAX_SIZE])
	}()

	var r bytes.Reader
	r.Reset(buf)
	var msg cachefiles_msg
	if err := struc.Unpack(&r, &msg); err != nil {
		return err
	}
	switch msg.OpCode {
	case CACHEFILES_OP_OPEN:
		var open cachefiles_open
		if err := struc.Unpack(&r, &open); err != nil {
			return err
		}
		return s.handleOpen(msg.MsgId, msg.ObjectId, open.Fd, open.Flags, open.VolumeKey, open.CookieKey)
	case CACHEFILES_OP_CLOSE:
		return s.handleClose(msg.MsgId, msg.ObjectId)
	case CACHEFILES_OP_READ:
		var read cachefiles_read
		if err := struc.Unpack(&r, &read); err != nil {
			return err
		}
		return s.handleRead(msg.MsgId, msg.ObjectId, read.Len, read.Off)
	default:
		return errors.New("unknown opcode")
	}
}

func (s *Server) handleOpen(msgId, objectId, fd, flags uint32, volume, cookie []byte) (retErr error) {
	// volume is "erofs,<domain_id>\x00" (domain_id is same as fsid if not specified)
	// cookie is "<fsid>"

	var cacheSize int64
	fsid := string(cookie)

	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic in open: %v", r)
		}
		if retErr != nil {
			cacheSize = -int64(unix.ENODEV)
		}
		reply := fmt.Sprintf("copen %d,%d", msgId, cacheSize)
		devfd := int(s.devnode.Load())
		if devfd == 0 {
			log.Println("closed cachefiles fd in middle of open")
			return
		}
		if _, err := unix.Write(devfd, []byte(reply)); err != nil {
			log.Println("failed write to devnode", err)
		}
		if cacheSize < 0 {
			unix.Close(int(fd))
		}
	}()

	if string(volume) != "erofs,"+s.cfg.CacheDomain+"\x00" {
		return fmt.Errorf("wrong domain %q", volume)
	}

	// slab or manifest
	if idx := strings.TrimPrefix(fsid, slabPrefix); idx != fsid {
		log.Println("open slab", idx, "as", objectId)
		slabId, err := strconv.Atoi(idx)
		if err != nil {
			return err
		}
		cacheSize, retErr = s.handleOpenSlab(msgId, objectId, fd, flags, common.TruncU16(slabId))
		return
	} else if idx := strings.TrimPrefix(fsid, slabImagePrefix); idx != fsid {
		log.Println("open slab image", idx, "as", objectId)
		slabId, err := strconv.Atoi(idx)
		if err != nil {
			return err
		}
		cacheSize, retErr = s.handleOpenSlabImage(msgId, objectId, fd, flags, common.TruncU16(slabId))
		return
	} else if len(fsid) == 32 {
		log.Println("open image", fsid, "as", objectId)
		cacheSize, retErr = s.handleOpenImage(msgId, objectId, fd, flags, fsid)
		return
	} else {
		return fmt.Errorf("bad fsid %q", fsid)
	}
}

func (s *Server) handleOpenSlab(msgId, objectId, fd, flags uint32, id uint16) (int64, error) {
	// record open state
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	state := &openFileState{
		writeFd: fd,
		tp:      typeSlab,
		slabId:  id,
	}
	s.cacheState[objectId] = state
	s.stateBySlab[id] = state
	return slabBytes, nil
}

func (s *Server) handleOpenSlabImage(msgId, objectId, fd, flags uint32, id uint16) (int64, error) {
	// record open state
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	state := &openFileState{
		writeFd: fd,
		tp:      typeSlabImage,
		slabId:  id,
	}
	s.cacheState[objectId] = state
	// always one block
	return 1 << s.blockShift, nil
}

func (s *Server) handleOpenImage(msgId, objectId, fd, flags uint32, cookie string) (int64, error) {
	ctx, _ := s.mountCtxMap.Get(cookie)
	if ctx == nil {
		return 0, fmt.Errorf("missing context in handleOpenImage for %s", cookie)
	}
	mountCtx, _ := fromMountCtx(ctx)
	if mountCtx == nil {
		return 0, fmt.Errorf("missing context in handleOpenImage for %s", cookie)
	}

	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	state := &openFileState{
		writeFd:   fd,
		tp:        typeImage,
		imageData: mountCtx.imageData,
	}
	s.cacheState[objectId] = state
	return mountCtx.imageSize, nil
}

func (s *Server) handleClose(msgId, objectId uint32) error {
	log.Println("close", objectId)
	s.stateLock.Lock()
	state := s.cacheState[objectId]
	if state == nil {
		s.stateLock.Unlock()
		log.Println("missing state for close")
		return nil
	}
	if state.tp == typeSlab {
		delete(s.stateBySlab, state.slabId)
	}
	var fds slabFds
	switch state.tp {
	case typeSlab, typeManifestSlab:
		fds = s.readfdBySlab[state.slabId]
		delete(s.readfdBySlab, state.slabId)
	}
	delete(s.cacheState, objectId)
	s.stateLock.Unlock()

	// do rest of cleanup outside lock
	s.closeState(state, fds)
	return nil
}

func (s *Server) closeState(state *openFileState, slabFds slabFds) {
	fds := []int{int(state.writeFd), slabFds.readFd, slabFds.cacheFd}
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

func (s *Server) handleRead(msgId, objectId uint32, ln, off uint64) (retErr error) {
	s.stateLock.Lock()
	state := s.cacheState[objectId]
	s.stateLock.Unlock()

	if state == nil {
		panic("missing state")
	}

	defer func() {
		_, _, e1 := unix.Syscall(unix.SYS_IOCTL, uintptr(state.writeFd), CACHEFILES_IOC_READ_COMPLETE, uintptr(msgId))
		if e1 != 0 && retErr == nil {
			retErr = fmt.Errorf("ioctl error %d", e1)
		}
	}()

	switch state.tp {
	case typeImage:
		// log.Printf("read image %5d: %2dk @ %#x", objectId, ln>>10, off)
		return s.handleReadImage(state, ln, off)
	case typeSlabImage:
		// log.Printf("read slab image %5d: %2dk @ %#x", objectId, ln>>10, off)
		return s.handleReadSlabImage(state, ln, off)
	case typeSlab:
		// log.Printf("read slab %5d: %2dk @ %#x", objectId, ln>>10, off)
		return s.handleReadSlab(state, ln, off)
	default:
		panic("bad state type")
	}
}

func (s *Server) handleReadImage(state *openFileState, _, _ uint64) error {
	if state.imageData == nil {
		return errors.New("got read request when already written image")
	}
	// always write whole thing
	// TODO: does this have to be page-aligned?
	_, err := unix.Pwrite(int(state.writeFd), state.imageData, 0)
	if err != nil {
		return err
	}
	state.imageData = nil
	return nil
}

func (s *Server) handleReadSlabImage(state *openFileState, ln, off uint64) error {
	var devid string
	if off == 0 {
		// only superblock needs this
		devid = slabPrefix + strconv.Itoa(int(state.slabId))
	}
	buf := s.chunkPool.Get(int(ln))
	defer s.chunkPool.Put(buf)

	b := buf[:ln]
	erofs.SlabImageRead(devid, slabBytes, s.blockShift, off, b)
	_, err := unix.Pwrite(int(state.writeFd), b, int64(off))
	return err
}

func (s *Server) handleReadSlab(state *openFileState, ln, off uint64) (retErr error) {
	s.stats.slabReads.Add(1)
	defer func() {
		if retErr != nil {
			s.stats.slabReadErrs.Add(1)
		}
	}()

	if ln > uint64(shift.MaxChunkShift.Size()) {
		return errors.New("got too big slab read")
	}

	slabId := state.slabId
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
	return s.requestChunk(ctx, erofs.SlabLoc{slabId, addr}, digest, sphps)
}

func (s *Server) mountSlabImage(slabId uint16) error {
	fsid := slabImagePrefix + strconv.Itoa(int(slabId))
	mountPoint := filepath.Join(s.cfg.CachePath, fsid)
	logMsg := "opened slab image file for"

	if mounted, err := isErofsMount(mountPoint); err != nil || !mounted {
		if err = os.MkdirAll(mountPoint, 0755); err != nil {
			return fmt.Errorf("error mkdir on slab image mountpoint %s: %w", mountPoint, err)
		}
		opts := fmt.Sprintf("domain_id=%s,fsid=%s", s.cfg.CacheDomain, fsid)
		if err = unix.Mount("none", mountPoint, "erofs", 0, opts); err != nil {
			return fmt.Errorf("error mounting slab image %s on %s: %w  (is the kernel built with EROFS_FS_ONDEMAND?)", fsid, mountPoint, err)
		}
		// unmount and mount again to force slab to be flushed to disk.
		// if anything else is mounted already this won't work, though it won't hurt either.
		// in that case, we assume this happened the first time.
		if err = unix.Unmount(mountPoint, 0); err != nil {
			return fmt.Errorf("error unmounting slab image %s on %s: %w", fsid, mountPoint, err)
		}
		if err = unix.Mount("none", mountPoint, "erofs", 0, opts); err != nil {
			return fmt.Errorf("error mounting slab image %s on %s: %w", fsid, mountPoint, err)
		}
		logMsg = "mounted and " + logMsg
	}

	slabFd, err := s.openSlabImageFile(mountPoint)
	if err != nil {
		_ = unix.Unmount(mountPoint, 0)
		return fmt.Errorf("error opening slab image file %s: %w", mountPoint, err)
	}

	cacheFd, err := s.openSlabBackingFile(slabId)
	if err != nil {
		_ = unix.Close(slabFd)
		_ = unix.Unmount(mountPoint, 0)
		return fmt.Errorf("error opening slab backing file %s: %w", mountPoint, err)
	}

	s.stateLock.Lock()
	s.readfdBySlab[slabId] = slabFds{slabFd, cacheFd}
	s.stateLock.Unlock()

	log.Println(logMsg, fsid)
	return nil
}

func (s *Server) setupFakeSlabImage(slabId uint16) error {
	// If we're not in on-demand mode, set up a plain file in the same place where cachefiles
	// would have put it, so that we can get fds to use. Also if this system does switch to
	// cachefiles later, it should just work from there.
	tag, totalBlocks := s.SlabInfo(slabId)
	backingPath := filepath.Join(s.cfg.CachePath, fscachePath(s.cfg.CacheDomain, tag))
	_ = os.MkdirAll(filepath.Dir(backingPath), 0o755)
	fd, err := unix.Open(backingPath, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return err
	}
	// this doesn't really matter, it might only matter for a transition to cachefiles
	_ = unix.Ftruncate(fd, int64(totalBlocks)<<s.blockShift)

	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	s.stateBySlab[slabId] = &openFileState{
		writeFd: common.TruncU32(fd), // write and read to same fd
		tp:      typeSlab,
		slabId:  slabId,
	}
	s.readfdBySlab[slabId] = slabFds{fd, fd}

	return nil
}

func (s *Server) openSlabImageFile(mountPoint string) (int, error) {
	slabFile := filepath.Join(mountPoint, "slab")
	slabFd, err := unix.Open(slabFile, unix.O_RDONLY, 0)
	if err != nil {
		return 0, err
	}

	// disable readahead so we don't get requests for parts we haven't written
	if err = unix.Fadvise(slabFd, 0, 0, unix.FADV_RANDOM); err != nil {
		_ = unix.Close(slabFd)
		return 0, fmt.Errorf("fadvise: %w", err)
	}

	return slabFd, nil
}

func (s *Server) openSlabBackingFile(slabId uint16) (int, error) {
	tag, _ := s.SlabInfo(slabId)
	backingPath := filepath.Join(s.cfg.CachePath, fscachePath(s.cfg.CacheDomain, tag))
	return unix.Open(backingPath, unix.O_RDWR, 0)
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
