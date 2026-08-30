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

	nbdserver "github.com/pojntfx/go-nbd/pkg/server"
	"golang.org/x/sys/unix"
)

type (
	nbdSlabBackend struct {
		s      *Server
		slabId uint16
	}
)

func (s *Server) setupNbdSock() error {
	// // FIXME: does this even work?
	// if fd, err:= s.cfg.FdStore.GetFd(savedFdName); err==nil{
	// 	log.Println("restored nbd socket")
	// 	s.nbdsock.Store(int32(fd))
	// 	return nil
	// }

	path := filepath.Join(s.cfg.CachePath, "nbdsock")
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	s.nbdsock.Store(l)
	log.Println("set up nbd listener")
	return nil
}

func (s *Server) nbdServer() {
	s.shutdownWait.Add(1)
	defer s.shutdownWait.Done()

	var exports []*nbdserver.Export
	for slabId := range uint16(1) {
		exports = append(exports, &nbdserver.Export{
			Name:        fmt.Sprintf("slab%d", slabId),
			Description: fmt.Sprintf("styx slab %d", slabId),
			Backend:     &nbdSlabBackend{s: s, slabId: slabId},
		})
	}

	l := s.nbdsock.Load().(net.Listener)
	for {
		conn, err := l.Accept()
		if err != nil {
			break
		}
		log.Print("new nbd connection")
		go func() {
			err := nbdserver.Handle(
				conn,
				exports,
				&nbdserver.Options{
					ReadOnly:           true,
					MinimumBlockSize:   4096,
					PreferredBlockSize: 4096,
					SupportsMultiConn:  true,
				})
			if err != nil {
				log.Println("nbd server err:", err)
			}
		}()
	}
	log.Print("nbd server shutting down")
	return

	// <-s.shutdownChan

	// log.Print("stopping workers")
	// f.Close()                          // cause all future reads to error
	// time.Sleep(100 * time.Millisecond) // FIXME: wait until all "readers" exit
	// close(ch)
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
	// FIXME: oh, maybe if we read through the lo then it works, the writeback is lo->backing,
	// not clone->lo?
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
