package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	// nbdclient "github.com/pojntfx/go-nbd/pkg/client"
	// nbdserver "github.com/pojntfx/go-nbd/pkg/server"

	"github.com/Merovius/nbd"
	"golang.org/x/sys/unix"
)

type (
	nbdSlabBackend struct {
		s      *Server
		slabId uint16
	}
)

func (s *Server) nbdConnect(slabId uint16) (*os.File, func(), func() error, error) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &nbdSlabBackend{s: s, slabId: slabId}
	idx, wait, err := nbd.Loopback(ctx, d, d.Size())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("nbd connect slab %d: %w", slabId, err)
	}

	path := fmt.Sprintf("/dev/nbd%d", idx)

	// create marker for udev rules
	// TODO: is this too late?
	defer s.markForUdev(path)()

	dev, err := os.OpenFile(path, os.O_RDONLY, 0o600)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open nbd %q: %w", path, err)
	}

	log.Println("nbd connected to slab", slabId)
	return dev, cancel, wait, nil
}

func (b *nbdSlabBackend) ReadAt(p []byte, off int64) (int, error) {
	// handle reserved blocks directly.
	// the kernel will probably probe the first block for a partition table.
	if off+int64(len(p)) <= (reservedBlocks<<b.s.blockShift) ||
		off >= slabBytes-(reservedBlocks<<b.s.blockShift) {
		clear(p)
		return len(p), nil
	}

	ctx := context.Background()
	err := b.s.handleReadSlab(
		ctx,
		b.slabId,
		p,
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

func (b *nbdSlabBackend) Size() uint64 {
	return slabBytes
}

func (b *nbdSlabBackend) Sync() error {
	return nil
}
