package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/lunixbochs/struc"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.etcd.io/bbolt"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

const (
	typeImage uint16 = iota
	typeSlab
)

const presentMask = 1 << 31

type (
	server struct {
		cfg         *Config
		digestBytes int
		blockShift  blkshift
		csread      manifester.ChunkStoreRead
		mcread      manifester.ChunkStoreRead
		fetcher     *fetchScheduler
		catalog     *catalog
		db          *bbolt.DB
		msgPool     *sync.Pool
		builder     *erofs.Builder
		devnode     int

		lock        sync.Mutex
		cacheState  map[uint32]*openFileState // object id -> state
		stateBySlab map[uint16]*openFileState // slab id -> state
		stateByFsid map[string]*openFileState // image id -> state
	}

	openFileState struct {
		fd uint32
		tp uint16

		// for slabs
		slabId  uint16
		cacheFd uint32

		// for images
		fsid        string
		imageSize   int64
		imageData   []byte // data from manifester to be written
		mappedImage []byte // mmapped backing file
	}

	Config struct {
		DevPath   string
		CachePath string

		StyxPubKeys []signature.PublicKey
		Params      pb.DaemonParams

		ErofsBlockShift int
		SmallFileCutoff int

		Workers         int
		ReadaheadChunks int
	}
)

var _ erofs.SlabManager = (*server)(nil)

// init stuff

func CachefilesServer(cfg Config) *server {
	s := &server{
		cfg:         &cfg,
		digestBytes: int(cfg.Params.Params.DigestBits >> 3),
		blockShift:  blkshift(cfg.ErofsBlockShift),
		csread:      manifester.NewChunkStoreReadUrl(cfg.Params.ChunkReadUrl, manifester.ChunkReadPath),
		mcread:      manifester.NewChunkStoreReadUrl(cfg.Params.ManifestCacheUrl, manifester.ManifestCachePath),
		catalog:     newCatalog(),
		msgPool:     &sync.Pool{New: func() any { return make([]byte, CACHEFILES_MSG_MAX_SIZE) }},
		builder:     erofs.NewBuilder(erofs.BuilderConfig{BlockShift: cfg.ErofsBlockShift}),
		cacheState:  make(map[uint32]*openFileState),
		stateBySlab: make(map[uint16]*openFileState),
		stateByFsid: make(map[string]*openFileState),
	}
	s.fetcher = newFetchScheduler(&cfg, s, s.csread)
	return s
}

func (s *server) openDb() (err error) {
	opts := bbolt.Options{
		NoFreelistSync: true,
		FreelistType:   bbolt.FreelistMapType,
	}
	s.db, err = bbolt.Open(path.Join(s.cfg.CachePath, dbFilename), 0644, &opts)
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
		cur := tx.Bucket(imageBucket).Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var img pb.DbImage
			if err := proto.Unmarshal(v, &img); err != nil {
				log.Print("unmarshal error iterating images", err)
				continue
			}
			s.catalog.add(img.StorePath)
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

func (s *server) openDevNode() (err error) {
	const name = "devnode"
	s.devnode, err = systemdGetFd(name)
	if err == nil {
		if _, err = unix.Write(s.devnode, []byte("restore")); err != nil {
			return
		}
		log.Println("restored cachefiles socket")
		return
	}

	s.devnode, err = unix.Open(s.cfg.DevPath, unix.O_RDWR, 0600)
	if err == unix.ENOENT {
		_ = unix.Mknod(s.cfg.DevPath, 0600|unix.S_IFCHR, 10<<8+122)
		s.devnode, err = unix.Open(s.cfg.DevPath, unix.O_RDWR, 0600)
	}
	if _, err = unix.Write(s.devnode, []byte("dir "+s.cfg.CachePath)); err != nil {
		return
	} else if _, err = unix.Write(s.devnode, []byte("tag "+cacheTag)); err != nil {
		return
	} else if _, err = unix.Write(s.devnode, []byte("bind ondemand")); err != nil {
		return
	}
	systemdSaveFd(name, s.devnode)
	log.Println("set up cachefiles socket")
	return
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
	return valOrErr(&img, err)
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
	socketPath := path.Join(s.cfg.CachePath, Socket)
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
	go http.Serve(l, mux)
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
			}
			w.WriteHeader(http.StatusInternalServerError)
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
				wEnc.Encode(&genericResp{Status: Status{Success: true}})
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
			wEnc.Encode(&genericResp{Status: Status{Success: false, Error: err.Error()}})
		}
		parts = append(parts, " -> ", err.Error())
		log.Print(parts...)
	}
}

func (s *server) handleMountReq(r *MountReq) (*genericResp, error) {
	if !reStorePath.MatchString(r.StorePath) {
		return nil, mwErr(http.StatusBadRequest, "invalid store path")
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
	s.catalog.add(r.StorePath)

	return nil, s.tryMount(r.StorePath, r.MountPoint)
}

func (s *server) tryMount(storePath, mountPoint string) error {
	sph, _, _ := strings.Cut(storePath, "-")

	_ = os.MkdirAll(mountPoint, 0o755)
	opts := fmt.Sprintf("domain_id=%s,fsid=%s", domainId, sph)
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

func (s *server) handleUmountReq(r *UmountReq) (*genericResp, error) {
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

func (s *server) handleGcReq(r *GcReq) (*genericResp, error) {
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
						nextAddr = truncU32(sb.Sequence())
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

func (s *server) Run() error {
	if err := s.setupEnv(); err != nil {
		return err
	}
	if err := s.openDb(); err != nil {
		return err
	}
	if err := s.initCatalog(); err != nil {
		return err
	}
	if err := s.openDevNode(); err != nil {
		return err
	}
	if err := s.startSocketServer(); err != nil {
		return err
	}

	systemdReady()

	go s.cachefilesServer()

	s.restoreMounts()

	// TODO: shutdown
	<-make(chan struct{})
	return nil
}

func (s *server) cachefilesServer() {
	wchan := make(chan []byte)
	for i := 0; i < s.cfg.Workers; i++ {
		go func() {
			for {
				s.handleMessage(<-wchan)
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
		fds[0] = unix.PollFd{Fd: int32(s.devnode), Events: unix.POLLIN}
		n, err := unix.Poll(fds, 3600*1000)
		if err != nil {
			log.Printf("error from poll: %v", err)
			errors++
			continue
		}
		if n != 1 {
			continue
		}
		// read until we get zero
		readAfterPoll := false
		for {
			buf := s.msgPool.Get().([]byte)
			n, err = unix.Read(s.devnode, buf)
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
}

func (s *server) handleMessage(buf []byte) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic: %v", r)
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
		if retErr != nil {
			cacheSize = -int64(unix.ENODEV)
		}
		reply := fmt.Sprintf("copen %d,%d", msgId, cacheSize)
		if _, err := unix.Write(s.devnode, []byte(reply)); err != nil {
			log.Println("failed write to devnode", err)
		}
		if cacheSize < 0 {
			unix.Close(int(fd))
		}
	}()

	if string(volume) != "erofs,"+domainId+"\x00" {
		return fmt.Errorf("wrong domain %q", volume)
	}

	// slab or manifest
	if strings.HasPrefix(fsid, slabPrefix) {
		log.Println("open slab", fsid, "as", objectId)
		slabId, err := strconv.Atoi(fsid[len(slabPrefix):])
		if err != nil {
			return err
		}
		cacheSize, retErr = s.handleOpenSlab(msgId, objectId, fd, flags, truncU16(slabId))
		if retErr == nil {
			go s.findBackingCacheFile(objectId, fsid, typeSlab, cacheSize)
		}
		return
	} else if len(fsid) == 32 {
		log.Println("open image", fsid, "as", objectId)
		cacheSize, retErr = s.handleOpenImage(msgId, objectId, fd, flags, fsid)
		if retErr == nil {
			go s.findBackingCacheFile(objectId, fsid, typeImage, cacheSize)
		}
		return
	} else {
		return fmt.Errorf("bad fsid %q", fsid)
	}
}

func (s *server) handleOpenSlab(msgId, objectId, fd, flags uint32, id uint16) (int64, error) {
	// record open state
	s.lock.Lock()
	defer s.lock.Unlock()
	state := &openFileState{
		fd:     fd,
		tp:     typeSlab,
		slabId: id,
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
		s.lock.Lock()
		defer s.lock.Unlock()
		state := &openFileState{
			fd:        fd,
			tp:        typeImage,
			fsid:      cookie,
			imageSize: img.ImageSize,
		}
		s.cacheState[objectId] = state
		s.stateByFsid[cookie] = state
		return img.ImageSize, nil
	}

	// convert to binary
	var sph Sph
	if n, err := nixbase32.Decode(sph[:], []byte(cookie)); err != nil || n != len(sph) {
		return 0, fmt.Errorf("cookie is not a valid nix store path hash")
	}

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
	sb, err := s.mcread.Get(ctx, mReq.CacheKey(), nil)
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
		} else if sb, err = io.ReadAll(zr); err != nil {
			return 0, err
		}
		log.Printf("got manifest for %s", cookie)
	}

	// verify signature and params
	entry, smParams, err := common.VerifyMessageAsEntry(s.cfg.StyxPubKeys, common.ManifestContext, sb)
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

	// unmarshal into manifest
	data := entry.InlineData
	if len(data) == 0 {
		log.Printf("loading chunked manifest data for %s", cookie)
		data, err = s.readChunkedData(entry)
	}

	var m pb.Manifest
	err = proto.Unmarshal(data, &m)
	if err != nil {
		return 0, fmt.Errorf("manifest unmarshal error: %w", err)
	}
	// transform manifest into image (allocate chunks)
	var image bytes.Buffer
	err = s.builder.BuildFromManifestWithSlab(ctx, &m, &image, s)
	if err != nil {
		return 0, fmt.Errorf("build image error: %w", err)
	}
	size := int64(image.Len())

	log.Printf("new image %s, %d bytes in manifest, %d bytes in erofs", cookie, entry.Size, size)

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
		imageSize: size,
		imageData: image.Bytes(),
	}
	s.cacheState[objectId] = state
	s.stateByFsid[cookie] = state

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
	if state.mappedImage != nil {
		unix.Munmap(state.mappedImage)
		state.mappedImage = nil
	}
	unix.Close(int(state.fd))
	if state.tp == typeSlab {
		delete(s.stateBySlab, state.slabId)
	} else if state.tp == typeImage {
		delete(s.stateByFsid, state.fsid)
	}
	delete(s.cacheState, objectId)
	return nil
}

func (s *server) handleRead(msgId, objectId uint32, ln, off uint64) error {
	s.lock.Lock()
	state := s.cacheState[objectId]
	s.lock.Unlock()

	if state == nil {
		panic("missing state")
	}

	defer func() {
		_, _, e1 := unix.Syscall(unix.SYS_IOCTL, uintptr(state.fd), CACHEFILES_IOC_READ_COMPLETE, uintptr(msgId))
		if e1 != 0 {
			fmt.Errorf("ioctl error %d", e1)
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
		target := addrKey(truncU32(off >> s.blockShift))
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

	ch := s.fetcher.Submit(slabId, addr, digest, splitSphs(sphs))
	return <-ch
}

func (s *server) findBackingCacheFile(objectId uint32, fsid string, tp uint16, size int64) {
	// This won't appear in the filesystem immediately, but it should appear soon.
	backingPath := s.cfg.CachePath + "/" + fscachePath(fsid)

	const maxAttempts = 50 // about 5 seconds
	var fd int
	err := errors.New("start")
	for attempt := 0; err != nil && attempt < maxAttempts; attempt++ {
		time.Sleep(time.Duration(100+10*attempt) * time.Millisecond)
		fd, err = unix.Open(backingPath, 0, unix.O_RDWR)
	}
	if err != nil {
		log.Printf("couldn't locate backing file for %d %q after %d attempts", fsid, maxAttempts)
	}

	if tp == typeSlab {
		// just keep the fd to do pread on
		s.lock.Lock()
		defer s.lock.Unlock()
		if state := s.cacheState[objectId]; state != nil {
			state.cacheFd = truncU32(fd)
		} else {
			unix.Close(fd)
		}
	} else if tp == typeImage {
		// mmap it to read directly
		defer unix.Close(fd)
		img, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
		if err != nil {
			log.Println("mmap error on backing file:", err)
			return
		}

		s.lock.Lock()
		defer s.lock.Unlock()
		if state := s.cacheState[objectId]; state != nil {
			state.mappedImage = img
		} else {
			unix.Munmap(img)
		}
	} else {
		panic("bad type")
	}
}

// interface to fetch scheduler
func (s *server) gotChunk(slabId uint16, addr uint32, b []byte) error {
	s.lock.Lock()
	state := s.stateBySlab[slabId]
	s.lock.Unlock()
	if state == nil {
		return errors.New("slab not loaded")
	}
	fd := int(state.fd)
	// don't use state anymore after here

	// round up to block size (slice is guaranteed to have capacity)
	toWrite := b[:s.blockShift.roundup(int64(len(b)))]

	// TODO: pass this all the way through?
	// if len(toWrite) < ln {
	// 	return fmt.Errorf("chunk underflowed requested len: %d < %d", len(toWrite), ln)
	// }

	off := int64(addr) << s.blockShift
	if n, err := unix.Pwrite(fd, toWrite, off); err != nil {
		return err
	} else if n != len(toWrite) {
		return fmt.Errorf("short write %d != %d", n, len(toWrite))
	}

	// record async
	go s.db.Batch(func(tx *bbolt.Tx) error {
		sb := tx.Bucket(slabBucket).Bucket(slabKey(slabId))
		if sb == nil {
			return errors.New("missing slab bucket")
		}
		return sb.Put(addrKey(presentMask|addr), []byte{})
	})

	return nil
}

func (s *server) findBase(sph Sph) (catalogResult, error) {
	return s.catalog.findBase(sph)
}

func (s *server) getChunkDiff(ctx context.Context, bases, reqs []byte) (io.ReadCloser, error) {
	reqBytes, err := json.Marshal(manifester.ChunkDiffReq{Bases: bases, Reqs: reqs})
	if err != nil {
		return nil, err
	}
	u := strings.TrimSuffix(s.cfg.Params.ChunkDiffUrl, "/") + manifester.ChunkDiffPath
	// TODO: use context here
	res, err := http.Post(u, "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	} else if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("chunk diff http status: %s", res.Status)
	}
	return res.Body, nil
}

func (s *server) getDigestsFromImage(fsid string) ([]*pb.Entry, error) {
	var img []byte
	s.lock.Lock()
	state := s.stateByFsid[fsid]
	if state != nil {
		img = state.mappedImage
	}
	s.lock.Unlock()

	if state == nil {
		return nil, errors.New("image not found")
	} else if img == nil {
		return nil, errors.New("backing file not mapped yet")
	}
	return erofs.ReadErofs(img)
}

// TODO: move this above "interface to fetch scheduler"
func (s *server) readChunkedData(entry *pb.Entry) ([]byte, error) {
	ctx := context.TODO()

	out := make([]byte, entry.Size)
	dest := out
	digests := entry.Digests

	var eg errgroup.Group
	eg.SetLimit(s.cfg.Workers)

	for len(digests) > 0 && len(dest) > 0 {
		digest := digests[:s.digestBytes]
		digests = digests[s.digestBytes:]
		toRead := min(len(dest), 1<<s.cfg.Params.Params.ChunkShift)
		buf := dest[:0]
		dest = dest[toRead:]

		// TODO: see if we can diff these chunks against some other chunks we have

		eg.Go(func() error {
			digestStr := common.DigestStr(digest)
			got, err := s.csread.Get(ctx, digestStr, buf)
			if err != nil {
				return err
			} else if len(got) != toRead {
				return fmt.Errorf("chunk was wrong size: %d vs %d", len(got), toRead)
			} else if err = checkChunkDigest(got, digest); err != nil {
				return err
			}
			return nil
		})
	}
	return valOrErr(out, eg.Wait())
}

func checkChunkDigest(got, digest []byte) error {
	h := sha256.New() // TODO: configurable
	h.Write(got)
	var gotDigest [sha256.Size]byte
	if !bytes.Equal(h.Sum(gotDigest[:0])[:len(digest)], digest) {
		return fmt.Errorf("chunk digest mismatch %x != %x", gotDigest, digest)
	}
	return nil
}

// slab manager

const (
	slabBytes  = 1 << 40
	slabBlocks = slabBytes >> 12
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

func (s *server) VerifyParams(hashBytes, blockSize, chunkSize int) error {
	if hashBytes != s.digestBytes || blockSize != int(s.blockShift.size()) || chunkSize != (1<<s.cfg.Params.Params.ChunkShift) {
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
			// reserve first block for metadata or whatever
			seq = 1
		}

		for i := range out {
			digest := digests[i*s.digestBytes : (i+1)*s.digestBytes]
			var id uint16
			var addr uint32
			if loc := cb.Get(digest); loc == nil { // allocate
				addr = truncU32(seq)
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
	return valOrErr(out, err)
}

func (s *server) SlabInfo(slabId uint16) (tag string, totalBlocks uint32) {
	return fmt.Sprintf("_slab_%d", slabId), slabBlocks
}
