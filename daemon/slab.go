package daemon

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/anatol/devmapper.go"
	"github.com/google/uuid"
	nbdclient "github.com/pojntfx/go-nbd/pkg/client"
	"golang.org/x/sys/unix"

	"github.com/freddierice/go-losetup/v2"
)

const slabBytes = 1 << 40
const metaBytes = 15 << 30 // kernel limit is 16 GiB

var invalidLo = losetup.New(99999, -99999)

type (
	slabState struct {
		tp uint16

		// clone and file slabs:
		readFd  int
		writeFd int

		// clone slabs only:
		size        int64
		regionBytes int32
		cloneLoaded bool
		cloneDev    bool
		metaName    string
		metaLo      losetup.Device
		dataName    string
		dataLo      losetup.Device
		nbdName     string
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
	}

	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	s.slabState[slabId] = st
	log.Println("set up file slab", slabId)
	return nil
}

func (s *Server) teardownFileSlabLocked(st *slabState) error {
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
	st.metaName = s.slabPath("meta", slabId)

	metaFd, err := unix.Open(st.metaName, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return fmt.Errorf("open meta %q: %w", st.metaName, err)
	}
	err = unix.Ftruncate(metaFd, metaBytes)
	if err != nil {
		return fmt.Errorf("truncate meta %q: %w", st.metaName, err)
	}
	err = unix.Close(metaFd)
	if err != nil {
		return fmt.Errorf("close meta %q: %w", st.metaName, err)
	}

	// TODO: maybe look for already-attached?
	st.metaLo, err = losetup.Attach(st.metaName, 0, false)
	if err != nil {
		return fmt.Errorf("losetup meta %q: %w", st.metaName, err)
	}

	// setup loopback for data file
	st.dataName = s.slabPath("data", slabId)

	// clone slab reads go to backing file
	st.readFd, err = unix.Open(st.dataName, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return fmt.Errorf("open data %q: %w", st.dataName, err)
	}
	err = unix.Ftruncate(st.readFd, slabBytes)
	if err != nil {
		return fmt.Errorf("truncate data %q: %w", st.dataName, err)
	}

	// TODO: maybe look for already-attached?
	st.dataLo, err = losetup.Attach(st.dataName, 0, false)
	if err != nil {
		return fmt.Errorf("losetup data %q: %w", st.dataName, err)
	}

	// setup nbd
	addr := s.nbdsock.Load().(net.Listener).Addr()
	nbdConn, err := net.Dial(addr.Network(), addr.String())
	if err != nil {
		return fmt.Errorf("nbd dial %v: %w", addr, err)
	}
	// TODO: fix race between find and connect (need to use netlink)
	st.nbdName, err = findFreeNbdDev()
	if err != nil {
		return fmt.Errorf("find free nbd: %w", err)
	}
	st.nbdDev, err = os.OpenFile(st.nbdName, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open nbd %q: %w", st.nbdName, err)
	}

	// create marker for udev rules
	defer s.markForUdev(st.nbdName)()

	nbdConnected := make(chan struct{})
	nbdErr := make(chan error, 1)
	log.Printf("nbd connecting to slab%d on %s", slabId, st.nbdName)
	go func() {
		runtime.LockOSThread() // TODO: figure out if this is really needed
		defer runtime.UnlockOSThread()

		nbdErr <- nbdclient.Connect(nbdConn, st.nbdDev, &nbdclient.Options{
			ExportName:  fmt.Sprintf("slab%d", slabId),
			Timeout:     0, // seconds, 0 means infinite
			OnConnected: func() { close(nbdConnected) },
		})
	}()
	select {
	case <-nbdConnected:
		log.Print("nbd connected")
	case err = <-nbdErr:
		return fmt.Errorf("nbd connect: %w", err)
	}

	// setup dm-clone
	clonePath, _ := s.SlabInfo(slabId)
	cloneName := filepath.Base(clonePath)
	tab := &devmapper.CloneTable{
		Start:       0,
		Length:      uint64(slabBytes),
		MetaDev:     st.metaLo.Path(),
		DestDev:     st.dataLo.Path(),
		SourceDev:   st.nbdName,
		RegionSize:  uint64(regionBytes),
		NoHydration: true,
	}
	devNum, err := devmapper.Create(cloneName, uuid.NewString())
	if err != nil {
		return fmt.Errorf("dm create %q: %w", cloneName, err)
	}
	defer s.markForUdev(fmt.Sprintf("dm-%d", unix.Minor(devNum)))()
	err = devmapper.Load(cloneName, 0, tab)
	if err != nil {
		return fmt.Errorf("dm load %q: %w", cloneName, err)
	}
	err = devmapper.Resume(cloneName)
	if err != nil {
		return fmt.Errorf("dm resume %q: %w", cloneName, err)
	}
	st.cloneLoaded = true

	// create our dev node
	_ = os.Remove(clonePath)
	err = unix.Mknod(clonePath, unix.S_IFBLK|0o600, int(devNum))
	if err != nil {
		return fmt.Errorf("mknod %q: %w", clonePath, err)
	}
	st.cloneDev = true

	// write fd: clone slab writes go through clone device to mark hydration
	st.writeFd, err = unix.Open(clonePath, unix.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("clone open %q: %w", clonePath, err)
	}

	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	s.slabState[slabId] = st
	log.Println("set up on-demand slab", slabId)
	return nil
}

func (s *Server) teardownCloneSlab(slabId uint16, st *slabState) error {
	clonePath, _ := s.SlabInfo(slabId)

	// write fd
	if st.writeFd >= 0 {
		unix.Close(st.writeFd)
		st.writeFd = -1
	}

	// dev node
	if st.cloneDev {
		err := os.Remove(clonePath)
		if err != nil {
			return err
		}
		st.cloneDev = false
	}

	// dm-clone
	if st.cloneLoaded {
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

	// read fd
	if st.readFd >= 0 {
		unix.Close(st.readFd)
		st.readFd = -1
	}

	// data loopback
	if st.dataLo != invalidLo {
		err := st.dataLo.Detach()
		if err != nil {
			return err
		}
		st.dataLo = invalidLo
	}

	// meta loopback
	if st.metaLo != invalidLo {
		err := st.metaLo.Detach()
		if err != nil {
			return err
		}
		st.metaLo = invalidLo
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
			time.Sleep(2 * time.Second)
			os.Remove(marker)
		}()
	}
}
