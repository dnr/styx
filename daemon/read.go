package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/erofs"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

// bridge nbd server to differ
func (s *Server) handleReadSlab(ctx context.Context, slabId uint16, p []byte, off uint64) (retErr error) {
	if len(p) == 0 {
		return nil
	}

	s.stats.slabReads.Add(1)
	defer func() {
		if retErr != nil {
			s.stats.slabReadErrs.Add(1)
		}
	}()

	// note that one read may not start at a chunk boundary, and may cross boundaries too.
	// so we may resolve it into multiple chunk reads. we shouldn't get reads for unmapped
	// ranges, but if we do, return zeros. the zeros won't be hydrated into the backing file so
	// it's okay (but may create problems with caching.. need to check this).
	rr := slabReadReq{
		slabId:       slabId,
		rdAddr:       common.TruncU32(off >> s.blockShift),
		rdEnd:        common.TruncU32((off+uint64(len(p))-1)>>s.blockShift + 1),
		p:            p,
		reqs:         make([]chunkReadReq, 0, 4),
		requestChunk: s.requestChunk,
		pread:        s.preadSlab,
		blockShift:   s.blockShift,
	}
	if err := s.db.View(rr.build); err != nil {
		return err
	} else if err = rr.run(ctx); err != nil {
		return err
	}
	return rr.fill()
}

func (s *Server) preadSlab(slabId uint16, p []byte, off int64) error {
	fd := s.getWriteFd(slabId)
	_, err := unix.Pread(fd, p, off)
	return err
}

type chunkReadReq struct {
	addr   uint32
	end    uint32
	digest cdig.CDig
	sphps  []SphPrefix
}

type slabReadReq struct {
	slabId        uint16
	rdAddr, rdEnd uint32
	p             []byte
	reqs          []chunkReadReq

	// can be mocked
	requestChunk func(context.Context, erofs.SlabLoc, cdig.CDig, []SphPrefix) error
	pread        func(uint16, []byte, int64) error
	blockShift   shift.Shift

	// valid during build only:
	sb, cb    *bbolt.Bucket
	cur       *bbolt.Cursor
	chunkAddr uint32
	chunkEnd  uint32
	dig       cdig.CDig
	err       error
}

func (rr *slabReadReq) build(tx *bbolt.Tx) error {
	defer func() { rr.sb, rr.cb, rr.cur = nil, nil, nil }()

	rr.sb = tx.Bucket(slabBucket).Bucket(slabKey(rr.slabId))
	rr.cb = tx.Bucket(chunkBucket)
	if rr.sb == nil || rr.cb == nil {
		return nil // missing buckets, no data yet
	}
	rr.cur = rr.sb.Cursor()

	// first find first chunk, then take chunks until we start past the end of our range
	for rr.seek(); rr.err == nil; rr.next() {
		if err := rr.addReq(); err != nil {
			return err
		}
	}

	if rr.err != nil && rr.err != io.EOF {
		return rr.err
	}
	return nil
}

// position cursor at first chunk that overlaps with read
func (rr *slabReadReq) seek() {
	target := addrKey(rr.rdAddr)
	k, v := rr.cur.Seek(target)
	if k == nil {
		k, v = rr.cur.Last()
	} else if !bytes.Equal(target, k) {
		// if we landed in between chunks, move back to the start of previous
		k, v = rr.cur.Prev()
		if k == nil {
			k, v = rr.cur.First() // there was no previous chunk
		}
	}
	rr.setAddrs(k, v)
	if rr.err != nil {
		return
	} else if rr.chunkAddr >= rr.rdEnd {
		rr.err = io.EOF // next chunk is past end of read range
	} else if rr.rdAddr >= rr.chunkEnd {
		rr.next() // initial offset was in a gap, move to next (may not exist)
	}
}

// move cursor to next chunk
func (rr *slabReadReq) next() {
	rr.setAddrs(rr.cur.Next())
	if rr.err == nil && rr.chunkAddr >= rr.rdEnd {
		rr.err = io.EOF // next chunk is past end of read range
	}
}

// sets rr.{chunkAddr,chunkEnd,dig} from k, v, or sets rr.err
func (rr *slabReadReq) setAddrs(k, v []byte) {
	if k == nil || k[0]&0x80 != 0 {
		rr.err = io.EOF // ran off start/end or into present map
	} else if len(v) < 2+cdig.Bytes {
		rr.err = errors.New("bad value in loc entry")
	} else {
		rr.chunkAddr = addrFromKey(k)
		var blocks uint16
		blocks, rr.dig = loadSlab(v)
		rr.chunkEnd = rr.chunkAddr + uint32(blocks)
	}
}

func (rr *slabReadReq) addReq() error {
	// look up digest to get store paths
	loc := rr.cb.Get(rr.dig[:])
	if loc == nil || len(loc) < 8 {
		return errors.New("missing digest->loc reference")
	} else if slabLoc, blocks := loadLocAndBlocks(loc); slabLoc.SlabId != rr.slabId {
		return fmt.Errorf("chunk loc slabid mismatch %d != %d", slabLoc.SlabId, rr.slabId)
	} else if slabLoc.Addr != rr.chunkAddr {
		return fmt.Errorf("chunk loc addr mismatch %d != %d", slabLoc.Addr, rr.chunkAddr)
	} else if uint32(blocks) != rr.chunkEnd-rr.chunkAddr {
		return fmt.Errorf("chunk loc len mismatch %d != %d", blocks, rr.chunkEnd-rr.chunkAddr)
	}
	req := chunkReadReq{
		addr:   rr.chunkAddr,
		end:    rr.chunkEnd,
		digest: rr.dig,
		sphps:  loadLocSphps(loc),
	}
	rr.reqs = append(rr.reqs, req)

	if len(req.sphps) == 0 {
		log.Println("missing sph references for", rr.slabId, req.addr, req.digest.String())
	}
	return nil
}

func (rr *slabReadReq) run(ctx context.Context) error {
	if len(rr.reqs) == 0 {
		return nil
	} else if len(rr.reqs) == 1 {
		req := rr.reqs[0]
		return rr.requestChunk(ctx, erofs.SlabLoc{rr.slabId, req.addr}, req.digest, req.sphps)
	}

	eg := errgroup.WithContext(ctx)
	for _, req := range rr.reqs {
		eg.Go(func() error {
			return rr.requestChunk(eg, erofs.SlabLoc{rr.slabId, req.addr}, req.digest, req.sphps)
		})
	}
	return eg.Wait()
}

func (rr *slabReadReq) fill() error {
	// we have now written to backing file through clone dev, but dm-clone requires that we
	// still perform the read ourselves. read through the clone device for now.
	// FIXME: pass this directly in memory?
	addr := rr.rdAddr
	for i, req := range rr.reqs {
		if req.addr < addr && i > 0 {
			return fmt.Errorf("bug: overlapping chunks? %d < %d", req.addr, addr)
		}
		if req.addr > addr {
			// gap
			toClear := rr.p[rr.addrInBytes(addr):rr.addrInBytes(req.addr)]
			clear(toClear)
			addr = req.addr
		}
		// read back from clone dev
		toRead := rr.p[rr.addrInBytes(addr):min(int64(len(rr.p)), rr.addrInBytes(req.end))]
		readOff := rr.offInBytes(addr)
		if err := rr.pread(rr.slabId, toRead, readOff); err != nil {
			return err
		}
		addr = req.end
	}
	if addr < rr.rdEnd {
		// end gap
		clear(rr.p[rr.addrInBytes(addr):])
	}
	return nil
}

func (rr *slabReadReq) addrInBytes(addr uint32) int64 {
	if rr.rdAddr > addr {
		panic("fill overflow error")
	}
	return int64(addr-rr.rdAddr) << rr.blockShift
}

func (rr *slabReadReq) offInBytes(off uint32) int64 {
	return int64(off) << rr.blockShift
}
