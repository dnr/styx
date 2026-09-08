package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/anatol/devmapper.go"
	"github.com/dnr/styx/erofs"
	"github.com/google/uuid"
	nbdclient "github.com/pojntfx/go-nbd/pkg/client"
	"golang.org/x/sys/unix"
)

const slabBytes = 1 << 40
const metaBytes = 15 << 30 // kernel limit is 16 GiB
// TODO: reconsider this size
const slabFlushChBufferSize = 10000
const slabFlushWait = 1 * time.Second
const slabFlushMax = 1000

type (
	slabState struct {
		tp uint16

		// clone and file slabs:
		readFd  int
		writeFd int
		flushCh chan erofs.SlabLoc

		// clone slabs only:
		size        int64
		regionBytes int32
		nbdDev      *os.File
	}
)

func (s *Server) getReadFd(slabId uint16) int {
	// TODO: remove locking overhead
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	if st, ok := s.slabState[slabId]; ok {
		return st.readFd
	}
	return -1
}

func (s *Server) getWriteFd(slabId uint16) int {
	// TODO: remove locking overhead
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	if st, ok := s.slabState[slabId]; ok {
		return st.writeFd
	}
	return -1
}

func (s *Server) slabPath(tp string, slabId uint16) string {
	return filepath.Join(s.cfg.CachePath, slabSubdir, fmt.Sprintf("slab%d%s", slabId, tp))
}

func (s *Server) slabDmName(slabId uint16) string {
	return fmt.Sprintf("styx-slab-%d", slabId)
}

func (s *Server) setupFileSlab(slabId uint16) error {
	dataName := s.slabPath("data", slabId)
	fd, err := unix.Open(dataName, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return err
	}

	st := &slabState{
		tp:      typeFileSlab,
		writeFd: fd,
		readFd:  fd,
		flushCh: make(chan erofs.SlabLoc, slabFlushChBufferSize),
	}

	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	s.slabState[slabId] = st
	go s.flusher(slabId, st)
	log.Println("set up file slab", slabId)
	return nil
}

func (s *Server) teardownFileSlabLocked(st *slabState) error {
	// FIXME: stop flusher goroutine
	return unix.Close(st.writeFd)
}

func (s *Server) setupCloneSlab(slabId uint16, slabBytes, regionBytes int64) (retErr error) {
	// serialize to avoid races with nbd device setup
	s.serializeSlabOps.Lock()
	defer s.serializeSlabOps.Unlock()

	st := &slabState{
		tp:      typeCloneSlab,
		size:    slabBytes,
		readFd:  -1,
		writeFd: -1,
		flushCh: make(chan erofs.SlabLoc, slabFlushChBufferSize),
	}
	defer func() {
		if retErr == nil {
			return
		}
		log.Printf("error setting up clone slab %d: %v", slabId, retErr)
		log.Print("trying to tear down...")
		if tdErr := s.teardownCloneSlab(slabId, st); tdErr != nil {
			log.Println("tear down:", tdErr)
		} else {
			log.Print("tear down ok")
		}
	}()

	// setup loopback for metadata
	metaPath := s.slabPath("meta", slabId)
	err := ensureRegularFileSize(metaPath, metaBytes)
	if err != nil {
		return fmt.Errorf("create/truncate meta %q: %w", metaPath, err)
	}
	metaLo, err := s.locache.findOrAttach(metaPath)
	if err != nil {
		return fmt.Errorf("losetup meta %q: %w", metaPath, err)
	}

	// setup loopback for data file
	dataPath := s.slabPath("data", slabId)

	// clone slab reads go to backing file
	err = ensureRegularFileSize(dataPath, slabBytes)
	if err != nil {
		return fmt.Errorf("create/truncate data %q: %w", dataPath, err)
	}

	dataLo, err := s.locache.findOrAttach(dataPath)
	if err != nil {
		return fmt.Errorf("losetup data %q: %w", dataPath, err)
	}

	st.readFd, err = unix.Open(dataLo.Path(), unix.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open data %q: %w", dataLo.Path(), err)
	}

	// setup nbd
	st.nbdDev, err = s.nbdConnect(slabId)
	if err != nil {
		return err
	}

	// setup dm-clone
	cloneName := s.slabDmName(slabId)
	tab := &devmapper.CloneTable{
		Start:       0,
		Length:      uint64(slabBytes),
		MetaDev:     metaLo.Path(),
		DestDev:     dataLo.Path(),
		SourceDev:   st.nbdDev.Name(),
		RegionSize:  uint64(regionBytes),
		NoHydration: true,
	}
	var devNo uint64
	di, err := devmapper.InfoByName(cloneName)
	if err == nil {
		// try to reuse previous
		devNo, err = di.DevNo, devmapper.Suspend(cloneName)
		if err != nil {
			return fmt.Errorf("dm suspend %q: %w", cloneName, err)
		}
	} else {
		// create it
		devNo, err = devmapper.Create(cloneName, uuid.NewString())
		if err != nil {
			return fmt.Errorf("dm create %q: %w", cloneName, err)
		}
	}
	defer s.markForUdev(devmapper.Path(devNo))()
	err = devmapper.Load(cloneName, 0, tab)
	if err != nil {
		return fmt.Errorf("dm load %q: %w", cloneName, err)
	}
	err = devmapper.Resume(cloneName)
	if err != nil {
		return fmt.Errorf("dm resume %q: %w", cloneName, err)
	}

	// write fd: clone slab writes go through clone device to mark hydration
	clonePath := devmapper.Path(devNo)
	// use O_DIRECT because we don't need an extra layer of block device caching here,
	// we want writes/reads to go to the underlying loopback directly.
	st.writeFd, err = unix.Open(clonePath, unix.O_RDWR|unix.O_DIRECT, 0o600)
	if err != nil {
		return fmt.Errorf("clone open %q: %w", clonePath, err)
	}

	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	s.slabState[slabId] = st
	go s.flusher(slabId, st)
	log.Println("set up on-demand slab", slabId)
	return nil
}

func (s *Server) teardownCloneSlab(slabId uint16, st *slabState) error {
	// FIXME: stop flusher goroutine

	// write fd
	if st.writeFd >= 0 {
		unix.Close(st.writeFd)
		st.writeFd = -1
	}

	// dm-clone+loopbacks:
	// We can't remove these completely because there are probably references (we're not
	// unmounting everything). We could leave the clones suspended but then even cached reads
	// wouldn't work. So just leave it running and the next process will fix it up.

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

	// read fd
	if st.readFd >= 0 {
		unix.Close(st.readFd)
		st.readFd = -1
	}

	return nil
}

func (s *Server) teardownSlabLocked(slabId uint16) error {
	var err error
	st, ok := s.slabState[slabId]
	if !ok {
		return nil
	}
	switch st.tp {
	case typeFileSlab:
		err = s.teardownFileSlabLocked(st)
	case typeCloneSlab:
		err = s.teardownCloneSlab(slabId, st)
	}
	if err != nil {
		return err
	}
	delete(s.slabState, slabId)
	return nil
}

func (s *Server) teardownSlabs() {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	for slabId := range s.slabState {
		err := s.teardownSlabLocked(slabId)
		if err != nil {
			log.Printf("error tearing down slab %d: %v", slabId, err)
		}
	}
}

// see services.udev.packages in module/default.nix
func (s *Server) markForUdev(dev string) func() {
	marker := filepath.Join(udevMarkerDir, filepath.Base(dev))
	_ = os.MkdirAll(filepath.Dir(marker), 0o700)
	if marker, err := os.Create(marker); err == nil {
		marker.Close()
	}
	return func() {
		go func() {
			time.Sleep(10 * time.Second)
			os.Remove(marker)
		}()
	}
}
