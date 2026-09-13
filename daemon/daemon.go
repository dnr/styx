package daemon

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/freddierice/go-losetup/v2"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"go.etcd.io/bbolt"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/common/systemd"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

const (
	typeFileSlab uint16 = iota
	typeCloneSlab
)

const (
	schemaV0 uint32 = iota
	schemaV1        // add catalog, shrunk sph in loc bytes
	schemaV2        // add length toslab and chunk values

	schemaLatest = schemaV2
)

const (
	savedFdName        = "nbdsock"
	presentMask        = 1 << 31
	reservedBlocks     = 16 // reserved at beginning and end of slab
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
		stats      daemonStats

		stateLock sync.Mutex
		slabState map[uint16]*slabState

		serializeSlabOps sync.Mutex

		imageSlabLo losetup.Device
		imageSlabF  *os.File

		// loopback device cache
		locache *locache

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

	// openFileState struct {
	// 	writeFd uint32 // for slabs, slab images, and store images
	// 	tp      uint16

	// 	// for slabs, slab images, and manifest slabs
	// 	slabId uint16
	// }

	Config struct {
		CachePath  string
		PublicSock string

		ErofsBlockShift int
		// SmallFileCutoff int

		RequestConcurrency   int
		NbdConnsPerSlab      int
		NbdServerConcurrency int

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
		slabState:       make(map[uint16]*slabState),
		locache:         newLoCache(),
		presentMap:      *common.NewSimpleSyncMap[erofs.SlabLoc, struct{}](),
		readKnownMap:    *common.NewSimpleSyncMap[erofs.SlabLoc, int](),
		diffMap:         make(map[erofs.SlabLoc]reqOp),
		recentReads:     make(map[string]*recentRead),
		diffSem:         semaphore.NewWeighted(int64(cfg.RequestConcurrency)),
		remanifestCache: *common.NewSimpleSyncMap[string, *remanifestCacheEntry](),
		shutdownChan:    make(chan struct{}),
	}
}

// TODO(file): do we need to condition this anymore?
func (s *Server) ondemand() bool {
	return true
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

func (s *Server) setupMountNamespace() error {
	// always ensure cache dir exists
	for _, subdir := range []string{
		slabSubdir,
	} {
		if err := os.MkdirAll(filepath.Join(s.cfg.CachePath, subdir), 0700); err != nil {
			return err
		}
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

// main server

func (s *Server) Start() error {
	if err := s.setupMountNamespace(); err != nil {
		return fmt.Errorf("error setting up mount namespaces: %w", err)
	}
	if err := s.openDb(); err != nil {
		return fmt.Errorf("error setting up database in %s: %w", s.cfg.CachePath, err)
	}

	s.locache.init()

	// TODO: get number of slabs from db and set them all up
	numSlabs := uint16(1)

	if s.ondemand() {
		for slabId := range numSlabs {
			// FIXME: region bytes config
			if err := s.setupCloneSlab(slabId, slabBytes, 4096); err != nil {
				return fmt.Errorf("error setting up clone slab %d: %w", slabId, err)
			}
		}
	} else {
		for slabId := range numSlabs {
			if err := s.setupFileSlab(slabId); err != nil {
				return fmt.Errorf("error setting up file slab %d: %w", slabId, err)
			}
		}
	}

	// manifest slab is always file
	if err := s.setupFileSlab(manifestSlabOffset); err != nil {
		return fmt.Errorf("error setting up manifest slab: %w", err)
	}

	// image slab
	if err := s.setupImageSlab(); err != nil {
		return fmt.Errorf("error setting up image slab: %w", err)
	}

	if err := s.startSocketServer(); err != nil {
		return err
	}

	if err := s.startFakeCacheServer(); err != nil {
		return err
	}

	go s.pruneRecentCaches()

	s.restoreMounts()

	s.cfg.FdStore.Ready()

	return nil
}

// this is only for tests! the real daemon doesn't clean up, since we can't restore the cache
// state, it dies and lets systemd keep the nbd socket open.
func (s *Server) Stop() {
	log.Print("stopping daemon...")
	close(s.shutdownChan)

	s.teardownSlabs()
	s.db.Close()

	log.Print("daemon shutdown done")
}
