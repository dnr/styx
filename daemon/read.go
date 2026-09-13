package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/erofs"
	"go.etcd.io/bbolt"
)

// bridge nbd server to differ
func (s *Server) handleReadSlab(ctx context.Context, slabId uint16, p []byte, off uint64) (retErr error) {
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
		slabId: slabId,
		rdAddr: common.TruncU32(off >> s.blockShift),
		rdEnd:  common.TruncU32((off + uint64(len(p))) >> s.blockShift),
		p:      p,
		reqs:   make([]chunkReadReq, 0, 4),
	}
	err := s.db.View(rr.build)
	if err != nil {
		return err
	}
	return rr.run(ctx, s)
}

type chunkReadReq struct {
	addr   uint32
	digest cdig.CDig
	sphps  []SphPrefix
}

type slabReadReq struct {
	slabId        uint16
	rdAddr, rdEnd uint32
	p             []byte
	reqs          []chunkReadReq

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
		return errors.New("missing buckets")
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
	}
	rr.setAddrs(k, v)
	if rr.err == nil && rr.rdAddr >= rr.chunkEnd {
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
	if loc == nil {
		return errors.New("missing digest->loc reference")
	}
	req := chunkReadReq{
		addr:   rr.chunkAddr,
		digest: rr.dig,
		sphps:  loadLocSphps(loc),
	}
	rr.reqs = append(rr.reqs, req)

	if len(req.sphps) == 0 {
		log.Println("missing sph references for", rr.slabId, req.addr, req.digest.String())
	}
	return nil
}

func (rr *slabReadReq) run(ctx context.Context, s *Server) error {
	if len(rr.reqs) == 1 {
		req := rr.reqs[0]
		return s.requestChunk(ctx, erofs.SlabLoc{rr.slabId, req.addr}, req.digest, req.sphps)
	}

	eg := errgroup.WithContext(ctx)
	for _, req := range rr.reqs {
		eg.Go(func() error {
			return s.requestChunk(eg, erofs.SlabLoc{rr.slabId, req.addr}, req.digest, req.sphps)
		})
	}
	return eg.Wait()
}
