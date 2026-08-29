package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/anatol/devmapper.go"
	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/erofs"
	"github.com/google/uuid"
	nbdclient "github.com/pojntfx/go-nbd/pkg/client"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"

	"github.com/freddierice/go-losetup/v2"
)

const slabBytes = 1 << 40
const metaBytes = 15 << 30 // kernel limit is 16 GiB

var invalidLo = losetup.New(99999, -99999)

type (
	slabState struct {
		slabId uint16
		tp     uint16
		size   int64

		// clone and file slabs
		readFd  int32
		writeFd int32

		// clone slabs only:
		regionBytes int32
		cloneLoaded bool
		metaName    string
		metaLo      losetup.Device
		dataName    string
		dataLo      losetup.Device
		nbdName     string
		nbdDev      *os.File
	}
)

func (s *Server) slabPath(tp string, slabId uint16) string {
	return filepath.Join(s.cfg.CachePath, "slabs", fmt.Sprintf("slab%d%s", slabId, tp))
}

func (s *Server) setupFileSlab(slabId uint16, slabBytes int64) error {
	st := &slabState{
		slabId: slabId,
		tp:     typeFileSlab,
	}
	// FIXME
	s.slabState[slabId] = st
	return errors.New("notimpl")
}

func (s *Server) setupCloneSlab(slabId uint16, slabBytes, regionBytes int64) (retErr error) {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	st := &slabState{
		slabId: slabId,
		tp:     typeCloneSlab,
		size:   slabBytes,
	}
	defer func() {
		if retErr == nil {
			return
		}
		log.Printf("error setting up clone slab %d: %v", slabId, retErr)
		log.Print("trying to tear down...")
		if tdErr := s.teardownCloneSlabSt(st); tdErr != nil {
			log.Println("tear down:", tdErr)
		} else {
			log.Print("tear down ok")
		}
	}()

	// setup loopback for metadata
	st.metaName = s.slabPath("meta", slabId)

	metaFd, err := unix.Open(dataName, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return err
	}
	err = unix.Ftruncate(metaFd, metaBytes)
	if err != nil {
		return err
	}
	err = unix.Close(metaFd)
	if err != nil {
		return err
	}

	st.metaLo, err := losetup.Attach(st.metaName, 0, false)
	if err != nil {
		return err
	}

	// setup loopback for data file
	st.dataName := s.slabPath("data", slabId)

	// clone slab reads from backing file
	st.readFd, err := unix.Open(dataName, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return err
	}
	err := unix.Ftruncate(st.readFd, slabBytes)
	if err != nil {
		return err
	}

	st.dataLo, err := losetup.Attach(dataName, 0, false)

	// setup nbd
	addr := s.nbdsock.Load().(net.Listener).Addr()
	nbdConn, err := net.Dial(addr.Network(), addr.String())
	// TODO: fix race between find and connect (use netlink)
	st.nbdName, err := findFreeNbdDev()
	st.nbdDev, err := os.OpenFile(nbdName, os.O_RDWR, 0o600)

	nbdConnected := make(chan struct{})
	nbdErr := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // TODO: figure out if this is really needed
		defer runtime.UnlockOSThread()

		nbdErr <- nbdclient.Connect(nbdConn, st.nbdDev, &nbdclient.Options{
			ExportName:  fmt.Sprintf("slab%d", slabId),
			Timeout:     10,
			OnConnected: func() { close(nbdConnected) },
		})
	}()
	select {
	case <-nbdConnected:
	case <-nbdErr:
		// return
	}

	// setup dm-clone
	clonePath, _ := s.SlabInfo(slabId)
	cloneName := filepath.Base(clonePath)
	tab := &devmapper.CloneTable{
		Start:       0,
		Length:      slabBytes,
		MetaDev:     st.metaName,
		DestDev:     dataName,
		SourceDev:   nbdName,
		RegionSize:  regionBytes,
		NoHydration: true,
	}
	err = devmapper.CreateAndLoad(cloneName, uuid.NewString(), 0, tab)
	st.cloneLoaded = true

	// clone slab writes through clone device to mark hydration
	st.writeFd := unix.Open(clonePath, unix.O_RDWR|unix.O_CREAT, 0o600)

	s.slabState[slabId] = st
	return nil
}

func (s *Server) teardownCloneSlab(slabId uint16) error {
	st, ok := s.slabState[slabId]
	if !ok {
		return
	}
	err := s.teardownCloneSlabSt(st)
	if err != nil {
		return err
	}
	delete(s.slabState[slabId])
	return nil
}

func (s *Server) teardownCloneSlabSt(st *slabState) error {
	// dm-clone
	if st.cloneLoaded {
		clonePath, _ := s.SlabInfo(slabId)
		cloneName := filepath.Base(clonePath)
		err := devmapper.Remove(cloneName)
		if err != nil {
			return err
		}
		st.cloneLoaded = false
	}

	// nbd
	if st.nbdDev != nil {
		err := nbdclient.Disconnect(st.nbdDev)
		if err != nil {
			return err
		}
		err = st.nbdDev.Close()
		if err != nil {
			return err
		}
		st.nbdDev = nil
	}

	// data
	if st.readFd >= 0 {
		unix.Close(st.readFd)
		st.readFd = -1
	}

	if st.dataLo != invalidLo {
		err := st.dataLo.Detach()
		if err != nil {
			return err
		}
		st.dataLo = invalidLo
	}

	// meta
	if st.metaLo != invalidLo {
		err := st.metaLo.Detach()
		if err != nil {
			return err
		}
		st.metaLo = invalidLo
	}

	return nil
}

// erofs.SlabManager interface
func (s *Server) VerifyParams(blockShift shift.Shift) error {
	if blockShift != s.blockShift {
		return errors.New("mismatched params")
	}
	return nil
}

// erofs.SlabManager interface
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

// erofs.SlabManager interface
func (s *Server) SlabInfo(slabId uint16) (tag string, totalBlocks uint32) {
	// len(tag) < 64
	return s.slabPath("clone", slabId), common.TruncU32(uint64(slabBytes) >> s.blockShift)
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
