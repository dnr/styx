package daemon

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"

	"github.com/dnr/styx/manifester"
)

type (
	fetchScheduler struct {
		cfg       *Config
		s         fetchServerInt
		csread    manifester.ChunkStoreRead
		chunkPool *sync.Pool
	}

	fetchServerInt interface {
		// b must have capacity up to at least a full block
		gotChunk(slabId uint16, addr uint32, b []byte) error
	}
)

func newFetchScheduler(
	cfg *Config,
	s fetchServerInt,
	csread manifester.ChunkStoreRead,
) *fetchScheduler {
	return &fetchScheduler{
		cfg:       cfg,
		s:         s,
		csread:    csread,
		chunkPool: &sync.Pool{New: func() any { return make([]byte, 1<<cfg.Params.Params.ChunkShift) }},
	}
}

func (d *fetchScheduler) Submit(slabId uint16, addr uint32, digest []byte, sphs []Sph) chan error {
	ctx := context.Background()
	ch := make(chan error)
	go d.readSingle(ctx, slabId, addr, digest, ch)
	return ch
}

func (d *fetchScheduler) readSingle(ctx context.Context, slabId uint16, addr uint32, digest []byte, ch chan error) (retErr error) {
	defer func() { ch <- retErr }()

	buf := d.chunkPool.Get().([]byte)
	defer d.chunkPool.Put(buf)

	digestStr := base64.RawURLEncoding.EncodeToString(digest)
	chunk, err := d.csread.Get(ctx, digestStr, buf[:0])
	if err != nil {
		return err
	} else if len(chunk) > len(buf) || &buf[0] != &chunk[0] {
		return fmt.Errorf("chunk overflowed chunk size: %d > %d", len(chunk), len(buf))
	} else if err = checkChunkDigest(chunk, digest); err != nil {
		return err
	}

	return d.s.gotChunk(slabId, addr, chunk)
}
