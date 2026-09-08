package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	nbdclient "github.com/pojntfx/go-nbd/pkg/client"
	nbdserver "github.com/pojntfx/go-nbd/pkg/server"
	"golang.org/x/sys/unix"
)

type (
	nbdSlabBackend struct {
		s      *Server
		slabId uint16
	}
)

func (s *Server) makeNbdServer(slabId uint16) (net.Conn, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	c1, err := net.FileConn(os.NewFile(uintptr(fds[0]), "nbd1"))
	if err != nil {
		return nil, err
	}
	c2, err := net.FileConn(os.NewFile(uintptr(fds[1]), "nbd2"))
	if err != nil {
		return nil, err
	}
	go s.nbdServer(slabId, c2)
	return c1, nil
}

func (s *Server) nbdServer(slabId uint16, conn net.Conn) {
	log.Println("starting nbd server for slab", slabId)
	err := nbdserver.Handle(
		conn,
		[]*nbdserver.Export{&nbdserver.Export{
			Name:        fmt.Sprintf("slab%d", slabId),
			Description: fmt.Sprintf("styx slab %d", slabId),
			Backend:     &nbdSlabBackend{s: s, slabId: slabId},
		}},
		&nbdserver.Options{
			ReadOnly:           true,
			MinimumBlockSize:   4096,
			PreferredBlockSize: 4096,
		})
	if err != nil {
		log.Println("nbd server err:", err)
	}
	log.Println("nbd server closed for slab", slabId)
}

func (s *Server) nbdConnect(slabId uint16) (*os.File, error) {
	// TODO: fix race between find and connect (need to use netlink)
	path, err := findFreeNbdDev()
	if err != nil {
		return nil, fmt.Errorf("find free nbd: %w", err)
	}
	dev, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open nbd %q: %w", path, err)
	}

	// create marker for udev rules
	defer s.markForUdev(path)()

	conn, err := s.makeNbdServer(slabId)
	if err != nil {
		return nil, err
	}

	connectedC := make(chan struct{})
	errC := make(chan error, 1)
	log.Printf("nbd connecting to slab %d on %s", slabId, path)
	go func() {
		err := nbdclient.Connect(conn, dev, &nbdclient.Options{
			ExportName:  fmt.Sprintf("slab%d", slabId),
			Timeout:     0, // seconds, 0 means infinite
			OnConnected: func() { close(connectedC) },
		})
		errC <- err
		log.Println("nbdclient.Connect returned", err)
	}()
	select {
	case <-connectedC:
		log.Println("nbd connected to slab", slabId)
		return dev, nil
	case err = <-errC:
		return nil, fmt.Errorf("nbd connect slab %d: %w", slabId, err)
	}
}

func (b *nbdSlabBackend) ReadAt(p []byte, off int64) (int, error) {
	// handle reserved blocks directly.
	// the kernel will probably probe the first block for a partition table.
	if off+int64(len(p)) <= (reservedBlocks<<b.s.blockShift) ||
		off >= slabBytes-(reservedBlocks<<b.s.blockShift) {
		clear(p)
		return len(p), nil
	}

	err := b.s.handleReadSlab(
		b.slabId,
		uint64(len(p)),
		uint64(off),
	)
	if err != nil {
		return 0, err
	}
	// we have now written to backing file through clone dev, but dm-clone requires that we
	// still perform the read ourselves. read through the clone device for now.
	// FIXME: pass this directly in memory?
	fd := b.s.getWriteFd(b.slabId)
	n, err := unix.Pread(fd, p, off)
	return n, err
}

func (b *nbdSlabBackend) WriteAt(p []byte, off int64) (int, error) {
	return 0, errors.New("read only")
}

func (b *nbdSlabBackend) Size() (int64, error) {
	return slabBytes, nil
}

func (b *nbdSlabBackend) Sync() error {
	return nil
}

func findFreeNbdDev() (string, error) {
	paths, err := filepath.Glob("/sys/class/block/nbd*")
	if err != nil {
		return "", err
	}

	// sort numerically
	sort.Slice(paths, func(i, j int) bool {
		return nbdIndex(paths[i]) < nbdIndex(paths[j])
	})

	for _, path := range paths {
		name := filepath.Base(path)

		// ignore things with suffixes
		if _, err := strconv.Atoi(strings.TrimPrefix(name, "nbd")); err != nil {
			continue
		}

		if _, err := os.Stat(filepath.Join(path, "pid")); errors.Is(err, fs.ErrNotExist) {
			dev := filepath.Join("/dev", name)
			if _, err := os.Stat(dev); err != nil {
				continue
			}
			return dev, nil
		} else if err != nil {
			return "", fmt.Errorf("stat %s: %w", path, err)
		}
	}

	return "", errors.New("no free NBD device")
}

func nbdIndex(path string) int {
	name := filepath.Base(path)
	n, err := strconv.Atoi(strings.TrimPrefix(name, "nbd"))
	if err != nil {
		return -1
	}
	return n
}
