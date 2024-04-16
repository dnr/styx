package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/dnr/styx/common"
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
		findBase(sph Sph) (catalogResult, error)
		getChunkDiff(ctx context.Context, bases, reqs []byte) (io.ReadCloser, error)
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
	go func() { ch <- d.diff(ctx, slabId, addr, digest, sphs) }()
	return ch
}

func (d *fetchScheduler) readSingle(ctx context.Context, slabId uint16, addr uint32, digest []byte) error {
	buf := d.chunkPool.Get().([]byte)
	defer d.chunkPool.Put(buf)

	digestStr := common.DigestStr(digest)
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

func (d *fetchScheduler) diff(ctx context.Context, slabId uint16, addr uint32, digest []byte, sphs []Sph) error {
	res, reqHash, ok := d.findBase(sphs)
	if !ok {
		log.Println("couldn't find base for chunk", common.DigestStr(digest))
		return d.readSingle(ctx, slabId, addr, digest)
	}

	log.Printf("diffing %s { %s -> %s }",
		res.reqName[:res.matchLen],
		res.baseName[res.matchLen:],
		res.reqName[res.matchLen:],
	)

	// res.baseHash -> reqHash
	// walk through base and pick KNOWN chunks
	// walk through req and pick #readahead UNKNOWN chunks following this requested one
	_ = reqHash
	var bases, reqs []byte
	var baseData []byte

	diff, err := d.s.getChunkDiff(ctx, bases, reqs)
	if err != nil {
		log.Println("chunk diff failed", err)
		return d.readSingle(ctx, slabId, addr, digest)
	}
	defer diff.Close()

	expand, err := d.expandChunkDiff(ctx, baseData, diff)
	_ = expand
	return nil
}

func (d *fetchScheduler) expandChunkDiff(ctx context.Context, baseData []byte, diff io.Reader) ([]byte, error) {
	// run zstd
	// requires file for base, but diff can be streaming
	// TODO: this really should be in-process

	baseFile, err := writeToTempFile(baseData)
	if err != nil {
		return nil, err
	}
	defer os.Remove(baseFile)

	zstd := exec.CommandContext(
		ctx,
		common.ZstdBin,
		"-c",                     // stdout
		"-d",                     // decode
		"--patch-from", baseFile, // base
	)
	zstd.Stdin = diff
	out, err := zstd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return nil, fmt.Errorf("zstd error: %w: %q", err, stderr)
	}

	return out, nil
}

func (d *fetchScheduler) findBase(sphs []Sph) (catalogResult, Sph, bool) {
	// find a base with similar data. go backwards on the assumption that recent images with
	// this chunk will be more similar.
	for i := len(sphs) - 1; i >= 0; i-- {
		if res, err := d.s.findBase(sphs[i]); err == nil {
			return res, sphs[i], true
		}
	}
	return catalogResult{}, Sph{}, false
}
