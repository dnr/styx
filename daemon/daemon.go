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
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/lunixbochs/struc"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/systemd"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

const (
	typeImage uint16 = iota
	typeSlab
)

const (
	savedFdName     = "devnode"
	preCreatedSlabs = 1
	presentMask     = 1 << 31
	reservedBlocks  = 4 // reserved at beginning of slab
)

type (
	server struct {
		cfg         *Config
		digestBytes int
		blockShift  common.BlkShift
		csread      manifester.ChunkStoreRead
		mcread      manifester.ChunkStoreRead
		catalog     *catalog
		db          *bbolt.DB
		msgPool     *sync.Pool
		chunkPool   *chunkPool
		builder     *erofs.Builder
		devnode     atomic.Int32

		lock        sync.Mutex
		cacheState  map[uint32]*openFileState // object id -> state
		stateBySlab map[uint16]*openFileState // slab id -> state

		shutdownChan chan struct{}
		shutdownWait sync.WaitGroup
	}

	openFileState struct {
		fd uint32
		tp uint16

		// for slabs
		slabId  uint16
		cacheFd uint32

		// for images
		fsid      string
		imageData []byte // data from manifester to be written
	}

	Config struct {
		DevPath     string
		CachePath   string
		CacheTag    string
		CacheDomain string

		StyxPubKeys []signature.PublicKey
		Params      pb.DaemonParams

		ErofsBlockShift int
		SmallFileCutoff int

		Workers         int
		ReadaheadChunks int

		IsTesting bool
	}
)

var _ erofs.SlabManager = (*server)(nil)

// init stuff

func CachefilesServer(cfg Config) *server {
	return &server{
		cfg:          &cfg,
		digestBytes:  int(cfg.Params.Params.DigestBits >> 3),
		blockShift:   common.BlkShift(cfg.ErofsBlockShift),
		csread:       manifester.NewChunkStoreReadUrl(cfg.Params.ChunkReadUrl, manifester.ChunkReadPath),
		mcread:       manifester.NewChunkStoreReadUrl(cfg.Params.ManifestCacheUrl, manifester.ManifestCachePath),
		catalog:      newCatalog(),
		msgPool:      &sync.Pool{New: func() any { return make([]byte, CACHEFILES_MSG_MAX_SIZE) }},
		chunkPool:    newChunkPool(int(cfg.Params.Params.ChunkShift)),
		builder:      erofs.NewBuilder(erofs.BuilderConfig{BlockShift: cfg.ErofsBlockShift}),
		cacheState:   make(map[uint32]*openFileState),
		stateBySlab:  make(map[uint16]*openFileState),
		shutdownChan: make(chan struct{}),
	}
}

func (s *server) openDb() (err error) {
	opts := bbolt.Options{
		NoFreelistSync: true,
		FreelistType:   bbolt.FreelistMapType,
	}
	s.db, err = bbolt.Open(filepath.Join(s.cfg.CachePath, dbFilename), 0644, &opts)
	if err != nil {
		return err
	}
	s.db.MaxBatchDelay = 100 * time.Millisecond

	return s.db.Update(func(tx *bbolt.Tx) error {
		var gp pb.GlobalParams
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
		} else if b := mb.Get(metaParams); b == nil {
			if b, err = proto.Marshal(s.cfg.Params.Params); err != nil {
				return err
			}
			mb.Put(metaParams, b)
		} else if err = proto.Unmarshal(b, &gp); err != nil {
			return err
		} else if mp := s.cfg.Params.Params; false ||
			gp.ChunkShift != mp.ChunkShift ||
			gp.DigestAlgo != mp.DigestAlgo ||
			gp.DigestBits != mp.DigestBits {
			return fmt.Errorf("mismatched global params; wipe cache and start over")
		}
		return nil
	})
}

func (s *server) initCatalog() (err error) {
	return s.db.View(func(tx *bbolt.Tx) error {
		cur := tx.Bucket(manifestBucket).Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var sm pb.SignedMessage
			if err := proto.Unmarshal(v, &sm); err != nil {
				log.Print("unmarshal error iterating manifests", err)
				continue
			}

			storePath := strings.TrimPrefix(sm.Msg.Path, common.ManifestContext+"/")
			spHash, spName, _ := strings.Cut(storePath, "-")

			s.catalog.add(storePath)

			if len(sm.Msg.InlineData) == 0 {
				var sph Sph
				if n, err := nixbase32.Decode(sph[:], []byte(spHash)); err != nil || n != len(sph) {
					continue
				}
				s.catalog.add(makeManifestSph(sph).String() + "-" + isManifestPrefix + spName)
			}
		}
		return nil
	})
}

func (s *server) setupEnv() error {
	err := exec.Command("modprobe", "cachefiles").Run()
	if err != nil {
		return err
	}
	return os.MkdirAll(s.cfg.CachePath, 0700)
}

func (s *server) openDevNode() (int, error) {
	log.Print("opening cachefiles node")
	fd, err := unix.Open(s.cfg.DevPath, unix.O_RDWR, 0600)
	if err == unix.ENOENT {
		_ = unix.Mknod(s.cfg.DevPath, 0600|unix.S_IFCHR, 10<<8+122)
		fd, err = unix.Open(s.cfg.DevPath, unix.O_RDWR, 0600)
	}
	if err != nil {
		return 0, err
	} else if _, err = unix.Write(fd, []byte("dir "+s.cfg.CachePath)); err != nil {
		unix.Close(fd)
		return 0, err
	} else if _, err = unix.Write(fd, []byte("tag "+s.cfg.CacheTag)); err != nil {
		unix.Close(fd)
		return 0, err
	} else if _, err = unix.Write(fd, []byte("bind ondemand")); err != nil {
		unix.Close(fd)
		return 0, err
	}
	return fd, nil
}

func (s *server) setupDevNode() error {
	fd, err := systemd.GetFd(savedFdName)
	if err == nil {
		if _, err = unix.Write(fd, []byte("restore")); err != nil {
			systemd.RemoveFd(savedFdName)
			unix.Close(fd)
			return err
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

	// if this is false, we'll create them and re-open and save that time
	if s.haveSlabFiles() {
		systemd.SaveFd(savedFdName, fd)
	}
	log.Println("set up cachefiles device")
	return nil
}

func (s *server) haveSlabFiles() bool {
	for i := uint16(0); i < preCreatedSlabs; i++ {
		tag, _ := s.SlabInfo(i)
		backingPath := filepath.Join(s.cfg.CachePath, fscachePath(s.cfg.CacheDomain, tag))
		if unix.Access(backingPath, unix.O_RDWR) != nil {
			return false
		}
	}
	return true
}

func (s *server) tryOpenSlabFiles() error {
	// create slabs by mounting them as images
	tmp, err := os.MkdirTemp("", "dummy")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for i := uint16(0); i < preCreatedSlabs; i++ {
		tag, _ := s.SlabInfo(i)
		opts := fmt.Sprintf("domain_id=%s,fsid=%s", s.cfg.CacheDomain, tag)
		log.Print("doing dummy mount to create slab")
		if err := unix.Mount("none", tmp, "erofs", 0, opts); err != nil {
			return err
		}
		if err := unix.Unmount(tmp, 0); err != nil {
			return err
		}
	}
	return nil
}

// socket server + mount management

// Reads a record in imageBucket.
func (s *server) readImageRecord(sph string) (*pb.DbImage, error) {
	var img pb.DbImage
	err := s.db.View(func(tx *bbolt.Tx) error {
		if buf := tx.Bucket(imageBucket).Get([]byte(sph)); buf != nil {
			if err := proto.Unmarshal(buf, &img); err != nil {
				return err
			}
		}
		return nil
	})
	return common.ValOrErr(&img, err)
}

// Does a transaction on a record in imageBucket. f should mutate its argument and return nil.
// If f returns an error, the record will not be written.
func (s *server) imageTx(sph string, f func(*pb.DbImage) error) error {
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

func (s *server) startSocketServer() (err error) {
	socketPath := filepath.Join(s.cfg.CachePath, Socket)
	os.Remove(socketPath)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: socketPath})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(MountPath, jsonmw(s.handleMountReq))
	mux.HandleFunc(UmountPath, jsonmw(s.handleUmountReq))
	mux.HandleFunc(GcPath, jsonmw(s.handleGcReq))
	mux.HandleFunc(DebugPath, jsonmw(s.handleDebugReq))
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

func jsonmw[reqT, resT any](f func(*reqT) (*resT, error)) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if r := recover(); r != nil {
				log.Println("http handler panic", r)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()

		w.Header().Set("Content-Type", "application/json")
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

		res, err := f(&req)

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

func (s *server) handleMountReq(r *MountReq) (*Status, error) {
	if !reStorePath.MatchString(r.StorePath) {
		return nil, mwErr(http.StatusBadRequest, "invalid store path or missing name")
	} else if r.Upstream == "" {
		return nil, mwErr(http.StatusBadRequest, "invalid upstream")
	} else if !strings.HasPrefix(r.MountPoint, "/") {
		return nil, mwErr(http.StatusBadRequest, "mount point must be absolute path")
	}
	sph, _, _ := strings.Cut(r.StorePath, "-")

	err := s.imageTx(sph, func(img *pb.DbImage) error {
		if img.MountState == pb.MountState_Mounted {
			// TODO: check if actually mounted in fs. if not, repair.
			return mwErr(http.StatusConflict, "already mounted")
		}
		img.StorePath = r.StorePath
		img.Upstream = r.Upstream
		img.MountState = pb.MountState_Requested
		img.MountPoint = r.MountPoint
		img.LastMountError = ""
		return nil
	})
	if err != nil {
		return nil, err
	}

	return nil, s.tryMount(r.StorePath, r.MountPoint)
}

func (s *server) tryMount(storePath, mountPoint string) error {
	sph, _, _ := strings.Cut(storePath, "-")

	_ = os.MkdirAll(mountPoint, 0o755)
	opts := fmt.Sprintf("domain_id=%s,fsid=%s", s.cfg.CacheDomain, sph)
	mountErr := unix.Mount("none", mountPoint, "erofs", 0, opts)

	_ = s.imageTx(sph, func(img *pb.DbImage) error {
		if mountErr == nil {
			img.MountState = pb.MountState_Mounted
			img.LastMountError = ""
		} else {
			img.MountState = pb.MountState_MountError
			img.LastMountError = mountErr.Error()
		}
		return nil
	})

	return mountErr
}

func (s *server) handleUmountReq(r *UmountReq) (*Status, error) {
	// allowed to leave out the name part here
	sph, _, _ := strings.Cut(r.StorePath, "-")

	var mp string
	err := s.imageTx(sph, func(img *pb.DbImage) error {
		if img.MountState != pb.MountState_Mounted {
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
		_ = s.imageTx(sph, func(img *pb.DbImage) error {
			img.MountState = pb.MountState_Unmounted
			img.MountPoint = ""
			return nil
		})
	}

	return nil, umountErr
}

func (s *server) handleGcReq(r *GcReq) (*Status, error) {
	// TODO
	return nil, errors.New("unimplemented")
}

func (s *server) handleDebugReq(r *DebugReq) (*DebugResp, error) {
	res := &DebugResp{
		Images: make(map[string]*pb.DbImage),
		Chunks: make(map[string]*DebugChunkInfo),
	}
	return res, s.db.View(func(tx *bbolt.Tx) error {
		// meta
		var gp pb.GlobalParams
		_ = proto.Unmarshal(tx.Bucket(metaBucket).Get(metaParams), &gp)
		res.Params = &gp
		// images
		cur := tx.Bucket(imageBucket).Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var img pb.DbImage
			if err := proto.Unmarshal(v, &img); err != nil {
				log.Print("unmarshal error iterating images", err)
				continue
			}
			img.StorePath = ""
			res.Images[img.StorePath] = &img
		}
		// slabs
		slabroot := tx.Bucket(slabBucket)
		cur = slabroot.Cursor()
		for k, _ := cur.First(); k != nil; k, _ = cur.Next() {
			blockSizes := make(map[uint32]uint32)
			sb := slabroot.Bucket(k)
			si := DebugSlabInfo{
				Index:         binary.BigEndian.Uint16(k),
				ChunkSizeDist: make(map[uint32]int),
			}
			scur := sb.Cursor()
			for sk, _ := scur.First(); sk != nil; {
				nextSk, _ := scur.Next()
				addr := addrFromKey(sk)
				if addr&presentMask == 0 {
					var nextAddr uint32
					if nextSk != nil && nextSk[0]&0x80 == 0 {
						nextAddr = addrFromKey(nextSk)
					} else {
						nextAddr = common.TruncU32(sb.Sequence())
					}
					blockSize := uint32(nextAddr - addr)
					blockSizes[addr] = blockSize
					si.TotalChunks++
					si.TotalBlocks += int(blockSize)
					si.ChunkSizeDist[blockSize]++
				} else {
					si.PresentChunks++
					si.PresentBlocks += int(blockSizes[addr&^presentMask])
				}
				sk = nextSk
			}
			res.Slabs = append(res.Slabs, &si)
		}
		// chunks
		cur = tx.Bucket(chunkBucket).Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var ci DebugChunkInfo
			ci.Slab, ci.Addr = loadLoc(v)
			sphs := v[6:]
			for len(sphs) > 0 {
				ci.StorePaths = append(ci.StorePaths, nixbase32.EncodeToString(sphs[:storepath.PathHashSize]))
				sphs = sphs[storepath.PathHashSize:]
			}
			ci.Present = slabroot.Bucket(slabKey(ci.Slab)).Get(addrKey(ci.Addr|presentMask)) != nil
			res.Chunks[common.DigestStr(k)] = &ci
		}
		return nil
	})
}

func (s *server) restoreMounts() {
	var toRestore []*pb.DbImage
	_ = s.db.View(func(tx *bbolt.Tx) error {
		cur := tx.Bucket(imageBucket).Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var img pb.DbImage
			if err := proto.Unmarshal(v, &img); err != nil {
				log.Print("unmarshal error iterating images", err)
				continue
			}
			if img.MountState == pb.MountState_Mounted {
				toRestore = append(toRestore, &img)
			}
		}
		return nil
	})
	for _, img := range toRestore {
		var st unix.Statfs_t
		err := unix.Statfs(img.MountPoint, &st)
		if err == nil && st.Type == erofs.EROFS_MAGIC {
			// log.Print("restoring: ", img.StorePath, " already mounted on ", img.MountPoint)
			continue
		}
		err = s.tryMount(img.StorePath, img.MountPoint)
		if err == nil {
			log.Print("restoring: ", img.StorePath, " restored to ", img.MountPoint)
		} else {
			log.Print("restoring: ", img.StorePath, " error: ", err)
		}
	}
}

// cachefiles server

func (s *server) Start() error {
	if err := s.setupEnv(); err != nil {
		return err
	}
	if err := s.openDb(); err != nil {
		return err
	}
	if err := s.initCatalog(); err != nil {
		return err
	}

	for i := 0; i < 2; i++ {
		if err := s.setupDevNode(); err != nil {
			return err
		}

		go s.cachefilesServer()

		if s.haveSlabFiles() {
			if i == 0 {
				break
			}
			return errors.New("could not create slab files")
		}

		// this is weird: when new files are opened by cachefiles, it keeps them as unlinked files
		// first, and doesn't link them into the fs until the cache is closed properly. we want
		// direct (backdoor) access to the backing file for the slab files, which we can only get
		// by opening them by name. to fix this, we need to force cachefiles to create them, then
		// close the cache and reopen it.
		if err := s.tryOpenSlabFiles(); err != nil {
			return err
		}
		s.stopCachefilesServer()
	}
	log.Print("cachefiles server ready")

	if err := s.startSocketServer(); err != nil {
		return err
	}

	systemd.Ready()

	s.restoreMounts()

	return nil
}

func (s *server) stopCachefilesServer() {
	log.Print("stopping cachefiles server...")
	fd := s.devnode.Load()
	s.devnode.Store(0)
	unix.Close(int(fd))
	s.shutdownWait.Wait()
	log.Print("stopped cachefiles server")
}

func (s *server) Stop() {
	close(s.shutdownChan)    // stops the socket server
	s.stopCachefilesServer() // stop cachefiles server. also waits for socket server
	s.db.Close()
	log.Print("daemon shutdown done")
}

func (s *server) cachefilesServer() {
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
					log.Printf("empty read")
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

func (s *server) handleMessage(buf []byte) (retErr error) {
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

func (s *server) handleOpen(msgId, objectId, fd, flags uint32, volume, cookie []byte) (retErr error) {
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
	if strings.HasPrefix(fsid, slabPrefix) {
		log.Println("open slab", fsid[len(slabPrefix):], "as", objectId)
		slabId, err := strconv.Atoi(fsid[len(slabPrefix):])
		if err != nil {
			return err
		}
		cacheSize, retErr = s.handleOpenSlab(msgId, objectId, fd, flags, common.TruncU16(slabId))
		return
	} else if len(fsid) == 32 {
		log.Println("open image", fsid, "as", objectId)
		cacheSize, retErr = s.handleOpenImage(msgId, objectId, fd, flags, fsid)
		return
	} else {
		return fmt.Errorf("bad fsid %q", fsid)
	}
}

func (s *server) handleOpenSlab(msgId, objectId, fd, flags uint32, id uint16) (int64, error) {
	// find backing file
	tag, _ := s.SlabInfo(id)
	backingPath := filepath.Join(s.cfg.CachePath, fscachePath(s.cfg.CacheDomain, tag))
	// we mostly just need read access, but open for write also so we can punch holes for gc
	cacheFd, err := unix.Open(backingPath, unix.O_RDWR, 0600)
	if err != nil {
		log.Println("failed to open backing file for slab", id)
		// TODO: if we _should_ be able to open it, return error
		// return 0, err
		cacheFd = 0
	}

	// record open state
	s.lock.Lock()
	defer s.lock.Unlock()
	state := &openFileState{
		fd:      fd,
		tp:      typeSlab,
		slabId:  id,
		cacheFd: common.TruncU32(cacheFd),
	}
	s.cacheState[objectId] = state
	s.stateBySlab[id] = state
	return slabBytes, nil
}

func (s *server) handleOpenImage(msgId, objectId, fd, flags uint32, cookie string) (int64, error) {
	// check if we have this image
	img, err := s.readImageRecord(cookie)
	if err != nil {
		return 0, err
	} else if img.Upstream == "" {
		return 0, errors.New("missing upstream")
	}
	if img.MountState != pb.MountState_Requested && img.MountState != pb.MountState_Mounted {
		log.Print("got open image request with bad mount state", img.String())
		// try to proceed anyway
	}
	if img.ImageSize > 0 {
		// we have it already
		s.lock.Lock()
		defer s.lock.Unlock()
		state := &openFileState{
			fd:   fd,
			tp:   typeImage,
			fsid: cookie,
		}
		s.cacheState[objectId] = state
		return img.ImageSize, nil
	}

	// convert to binary
	var sph Sph
	if n, err := nixbase32.Decode(sph[:], []byte(cookie)); err != nil || n != len(sph) {
		return 0, fmt.Errorf("cookie is not a valid nix store path hash")
	}
	// use a separate "sph" for the manifest itself (a single entry). only used if manifest is chunked.
	manifestSph := makeManifestSph(sph)

	ctx := context.Background()
	ctx = context.WithValue(ctx, "sph", sph)

	// get manifest

	// check cached first
	gParams := s.cfg.Params.Params
	mReq := manifester.ManifestReq{
		Upstream:        img.Upstream,
		StorePathHash:   cookie,
		ChunkShift:      int(gParams.ChunkShift),
		DigestAlgo:      gParams.DigestAlgo,
		DigestBits:      int(gParams.DigestBits),
		SmallFileCutoff: s.cfg.SmallFileCutoff,
	}
	envelopeBytes, err := s.mcread.Get(ctx, mReq.CacheKey(), nil)
	if err == nil {
		log.Printf("got manifest for %s from cache", cookie)
	} else {
		// not found cached, request it
		log.Printf("requesting manifest for %s", cookie)
		u := strings.TrimSuffix(s.cfg.Params.ManifesterUrl, "/") + manifester.ManifestPath
		reqBytes, err := json.Marshal(mReq)
		if err != nil {
			return 0, err
		}
		res, err := http.Post(u, "application/json", bytes.NewReader(reqBytes))
		if err != nil {
			return 0, fmt.Errorf("manifester http error: %w", err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("manifester http status: %s", res.Status)
		} else if zr, err := zstd.NewReader(res.Body); err != nil {
			return 0, err
		} else if envelopeBytes, err = io.ReadAll(zr); err != nil {
			return 0, err
		}
		log.Printf("got manifest for %s", cookie)
	}

	// verify signature and params
	entry, smParams, err := common.VerifyMessageAsEntry(s.cfg.StyxPubKeys, common.ManifestContext, envelopeBytes)
	if err != nil {
		return 0, err
	}
	if smParams != nil {
		match := smParams.ChunkShift == gParams.ChunkShift &&
			smParams.DigestBits == gParams.DigestBits &&
			smParams.DigestAlgo == gParams.DigestAlgo
		if !match {
			return 0, fmt.Errorf("chunked manifest global params mismatch")
		}
	}

	// check entry path to get storepath
	storePath := strings.TrimPrefix(entry.Path, common.ManifestContext+"/")
	if storePath != img.StorePath {
		return 0, fmt.Errorf("envelope storepath != requested storepath: %q != %q", storePath != img.StorePath)
	}
	spHash, spName, _ := strings.Cut(storePath, "-")
	if spHash != cookie || len(spName) == 0 {
		return 0, fmt.Errorf("invalid or mismatched name in manifest %q", storePath)
	}

	// record signed manifest message in db
	if err = s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(manifestBucket).Put([]byte(cookie), envelopeBytes)
	}); err != nil {
		return 0, err
	}
	// update catalog with this envelope (and manifest entry). should match code in initCatalog.
	s.catalog.add(storePath)

	// get payload or load from chunks
	data := entry.InlineData
	if len(data) == 0 {
		s.catalog.add(manifestSph.String() + "-" + isManifestPrefix + spName)

		log.Printf("loading chunked manifest for %s", storePath)

		// allocate space for manifest chunks in slab
		blocks := common.MakeBlocksList(entry.Size, common.BlkShift(s.cfg.Params.Params.ChunkShift), s.blockShift)

		ctxForManifestChunks := context.WithValue(ctx, "sph", manifestSph)
		locs, err := s.AllocateBatch(ctxForManifestChunks, blocks, entry.Digests)
		if err != nil {
			return 0, err
		}

		// read them out
		data, err = s.readChunks(entry.Size, locs, entry.Digests, []Sph{manifestSph}, true)
		if err != nil {
			return 0, err
		}
	}

	// unmarshal into manifest
	var m pb.Manifest
	err = proto.Unmarshal(data, &m)
	if err != nil {
		return 0, fmt.Errorf("manifest unmarshal error: %w", err)
	}

	// make sure this matches the name in the envelope and original request
	if niStorePath := path.Base(m.Meta.GetNarinfo().GetStorePath()); niStorePath != storePath {
		return 0, fmt.Errorf("narinfo storepath != envelope storepath: %q != %q", niStorePath != storePath)
	}

	// transform manifest into image (allocate chunks)
	var image bytes.Buffer
	err = s.builder.BuildFromManifestWithSlab(ctx, &m, &image, s)
	if err != nil {
		return 0, fmt.Errorf("build image error: %w", err)
	}
	size := int64(image.Len())

	log.Printf("new image %s: %d envelope, %d manifest, %d erofs", storePath, len(envelopeBytes), entry.Size, size)

	// record in db
	err = s.imageTx(cookie, func(img *pb.DbImage) error {
		img.ImageSize = size
		img.ManifestSize = entry.Size
		img.Meta = m.Meta
		return nil
	})
	if err != nil {
		return 0, err
	}

	// record open state
	s.lock.Lock()
	defer s.lock.Unlock()
	state := &openFileState{
		fd:        fd,
		tp:        typeImage,
		fsid:      cookie,
		imageData: image.Bytes(),
	}
	s.cacheState[objectId] = state

	return size, nil
}

func (s *server) handleClose(msgId, objectId uint32) error {
	log.Println("close", objectId)
	s.lock.Lock()
	defer s.lock.Unlock()
	state := s.cacheState[objectId]
	if state == nil {
		return nil
	}
	if state.cacheFd != 0 {
		unix.Close(int(state.cacheFd))
	}
	unix.Close(int(state.fd))
	if state.tp == typeSlab {
		delete(s.stateBySlab, state.slabId)
	}
	delete(s.cacheState, objectId)
	return nil
}

func (s *server) handleRead(msgId, objectId uint32, ln, off uint64) (retErr error) {
	s.lock.Lock()
	state := s.cacheState[objectId]
	s.lock.Unlock()

	if state == nil {
		panic("missing state")
	}

	defer func() {
		_, _, e1 := unix.Syscall(unix.SYS_IOCTL, uintptr(state.fd), CACHEFILES_IOC_READ_COMPLETE, uintptr(msgId))
		if e1 != 0 && retErr == nil {
			retErr = fmt.Errorf("ioctl error %d", e1)
		}
	}()

	switch state.tp {
	case typeImage:
		log.Printf("read image %5d: %2dk @ %#x", objectId, ln>>10, off)
		return s.handleReadImage(state, ln, off)
	case typeSlab:
		log.Printf("read slab %5d: %2dk @ %#x", objectId, ln>>10, off)
		return s.handleReadSlab(state, ln, off)
	default:
		panic("bad state type")
	}
}

func (s *server) handleReadImage(state *openFileState, _, _ uint64) error {
	if state.imageData == nil {
		return errors.New("got read request when already written image")
	}
	// always write whole thing
	_, err := unix.Pwrite(int(state.fd), state.imageData, 0)
	if err != nil {
		return err
	}
	state.imageData = nil
	return nil
}

func (s *server) handleReadSlab(state *openFileState, ln, off uint64) error {
	if ln > (1 << s.cfg.Params.Params.ChunkShift) {
		panic("got too big slab read")
	}

	if off == 0 && ln == 4096 {
		// this is kind of wacky, but make the first block look like an empty erofs filesystem
		// so we can mount it.
		_, err := unix.Pwrite(int(state.fd), emptyErofs, 0)
		return err
	}

	slabId := state.slabId
	var addr uint32
	digest := make([]byte, s.digestBytes)
	var sphs []byte

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
		}
		// take addr from key so we write at the right place even if read was in the middle of a chunk
		addr = addrFromKey(k)
		copy(digest, v)
		// look up digest to get store paths
		loc := tx.Bucket(chunkBucket).Get(digest)
		if loc == nil {
			return errors.New("missing digest->loc reference")
		}
		sphs = bytes.Clone(loc[6:])
		return nil
	})
	if err != nil {
		return err
	}

	ch := s.requestChunk(slabId, addr, digest, splitSphs(sphs))
	return <-ch
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
	loc := make([]byte, 6+len(sph))
	binary.LittleEndian.PutUint16(loc, id)
	binary.LittleEndian.PutUint32(loc[2:], addr)
	copy(loc[6:], sph[:])
	return loc
}

func loadLoc(b []byte) (id uint16, addr uint32) {
	return binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[2:])
}

func appendSph(loc []byte, sph Sph) []byte {
	sphs := loc[6:]
	for len(sphs) >= storepath.PathHashSize {
		if bytes.Equal(sphs[:storepath.PathHashSize], sph[:]) {
			return nil
		}
		sphs = sphs[storepath.PathHashSize:]
	}
	newLoc := make([]byte, len(loc)+len(sph))
	copy(newLoc, loc)
	copy(newLoc[len(loc):], sph[:])
	return newLoc
}

func (s *server) VerifyParams(hashBytes int, blockShift, chunkShift common.BlkShift) error {
	if hashBytes != s.digestBytes || blockShift != s.blockShift || chunkShift != common.BlkShift(s.cfg.Params.Params.ChunkShift) {
		return errors.New("mismatched params")
	}
	return nil
}

func (s *server) AllocateBatch(ctx context.Context, blocks []uint16, digests []byte) ([]erofs.SlabLoc, error) {
	sph := ctx.Value("sph").(Sph)

	n := len(blocks)
	out := make([]erofs.SlabLoc, n)
	err := s.db.Update(func(tx *bbolt.Tx) error {
		cb, slabroot := tx.Bucket(chunkBucket), tx.Bucket(slabBucket)
		sb, err := slabroot.CreateBucketIfNotExists(slabKey(0))
		if err != nil {
			return err
		}

		seq := sb.Sequence()
		if seq == 0 {
			// reserve some blocks for future purposes
			seq = reservedBlocks
		}

		for i := range out {
			digest := digests[i*s.digestBytes : (i+1)*s.digestBytes]
			var id uint16
			var addr uint32
			if loc := cb.Get(digest); loc == nil { // allocate
				addr = common.TruncU32(seq)
				seq += uint64(blocks[i])
				if err := cb.Put(digest, locValue(id, addr, sph)); err != nil {
					return err
				} else if err = sb.Put(addrKey(addr), digest); err != nil {
					return err
				}
			} else {
				if newLoc := appendSph(loc, sph); newLoc != nil {
					if err := cb.Put(digest, newLoc); err != nil {
						return err
					}
				}
				id, addr = loadLoc(loc)
			}
			out[i].SlabId = id
			out[i].Addr = addr

			// TODO: check seq for overflow here and move to next slab
		}

		return sb.SetSequence(seq)
	})
	return common.ValOrErr(out, err)
}

func (s *server) SlabInfo(slabId uint16) (tag string, totalBlocks uint32) {
	return fmt.Sprintf("_slab_%d", slabId), common.TruncU32(uint64(slabBytes) >> s.blockShift)
}

// like AllocateBatch but only lookup
func (s *server) lookupLocs(digests []byte) ([]erofs.SlabLoc, error) {
	out := make([]erofs.SlabLoc, len(digests)/s.digestBytes)
	err := s.db.View(func(tx *bbolt.Tx) error {
		cb := tx.Bucket(chunkBucket)
		for i := range out {
			digest := digests[i*s.digestBytes : (i+1)*s.digestBytes]
			loc := cb.Get(digest)
			if loc == nil {
				return errors.New("missing")
			}
			out[i].SlabId, out[i].Addr = loadLoc(loc)
		}
		return nil
	})
	return common.ValOrErr(out, err)
}
