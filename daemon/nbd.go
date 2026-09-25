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
)

type (
	nbdSlabBackend struct {
		s      *Server
		slabId uint16
		ctx    context.Context
		ready  <-chan struct{}
	}
)

func (s *Server) nbdConnect(slabId uint16, ready <-chan struct{}) (*os.File, func(), func() error, error) {
	ctx, cancel := context.WithCancel(s.shutdownCtx)
	d := &nbdSlabBackend{s: s, slabId: slabId, ctx: ctx, ready: ready}
	opts := nbd.LoopbackOpts{
		ReadOnly:   true,
		MultiConns: s.cfg.NbdConnsPerSlab,
		ServerOpts: nbd.ServerOpts{
			Concurrency: s.cfg.NbdServerConcurrency,
			AllocBuf:    s.chunkPool.Get,
			ReleaseBuf:  s.chunkPool.Put,
		},
	}
	idx, wait, err := nbd.Loopback(ctx, d, d.Size(), opts)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("nbd connect slab %d: %w", slabId, err)
	}

	path := fmt.Sprintf("/dev/nbd%d", idx)

	// create marker for udev rules
	// TODO: is this too late?
	defer s.markForUdev(path)()

	dev, err := os.OpenFile(path, os.O_RDONLY, 0o600)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("open nbd %q: %w", path, err)
	}

	log.Println("nbd connected to slab", slabId)
	return dev, cancel, wait, nil
}

func (b *nbdSlabBackend) ReadAt(p []byte, off int64) (int, error) {
	// handle reads of actual reserved areas specially so we can satisfy them during setup,
	// where various locks may be held. these reads come directly from the kernel for probing
	// partitions.
	prefixBytes := int64(reservedBlocks) << b.s.blockShift
	if off >= 0 && off <= prefixBytes && int64(len(p)) <= prefixBytes-off ||
		off >= int64(b.Size())-prefixBytes {
		clear(p)
		return len(p), nil
	}

	select {
	case <-b.ready:
	case <-b.ctx.Done():
		return 0, context.Cause(b.ctx)
	}

	err := b.s.handleReadSlab(
		b.ctx,
		b.slabId,
		p,
		uint64(off),
	)
	if err != nil {
		log.Printf("read error: slab %d, off %d, ln %d, err %v", b.slabId, off, len(p), err)
		return 0, err
	}
	return len(p), nil
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
