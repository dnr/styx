package daemon

import (
	"context"
	"log"
	"net/http"
	"slices"

	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

type (
	gcCtx struct {
		context.Context
		GcReq
		*GcResp
		tx *bbolt.Tx

		ib, cb, mb *bbolt.Bucket

		keepImage map[string]struct{}    // sph string
		keepSphps map[SphPrefix]struct{} // sph prefix
		keepDig   map[cdig.CDig]struct{}
	}

	rewriteChunk struct {
		d cdig.CDig
		v []byte
	}
)

func (s *Server) handleGcReq(ctx context.Context, r *GcReq) (*GcResp, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}

	tx, err := s.db.Begin(true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	resp := &GcResp{
		DeletedByState:   make(map[pb.MountState]int),
		RemainingByState: make(map[pb.MountState]int),
	}
	g := &gcCtx{
		Context:   ctx,
		GcReq:     *r,
		GcResp:    resp,
		tx:        tx,
		ib:        tx.Bucket(imageBucket),
		cb:        tx.Bucket(chunkBucket),
		mb:        tx.Bucket(manifestBucket),
		keepImage: make(map[string]struct{}, 1000),
		keepSphps: make(map[SphPrefix]struct{}, 1000),
		keepDig:   make(map[cdig.CDig]struct{}, 100000),
	}

	// use image bucket as roots
	ibcur := g.ib.Cursor()
	for k, v := ibcur.First(); k != nil; k, v = ibcur.Next() {
		var img pb.DbImage
		if proto.Unmarshal(v, &img) == nil {
			err := s.gcTraceImage(g, string(v), &img)
			if err != nil {
				return nil, err
			}
		}
	}

	// find images to delete
	var delImages [][]byte
	for k, _ := ibcur.First(); k != nil; k, _ = ibcur.Next() {
		if _, ok := g.keepImage[string(k)]; !ok {
			delImages = append(delImages, k)
		}
	}

	// find manifests to delete
	var delManifests [][]byte
	mbcur := g.mb.Cursor()
	for k, _ := mbcur.First(); k != nil; k, _ = mbcur.Next() {
		if _, ok := g.keepImage[string(k)]; !ok {
			delManifests = append(delManifests, k)
		}
	}

	// find all chunks to delete
	var delBlocks, remainBlocks, remainChunks int64
	var delChunks []cdig.CDig
	var delLocs []erofs.SlabLoc
	var rewriteChunks []rewriteChunk
	cbcur := g.cb.Cursor()
	for k, v := cbcur.First(); k != nil; k, v = cbcur.Next() {
		d := cdig.FromBytes(k)
		if _, ok := g.keepDig[d]; !ok {
			delChunks = append(delChunks, d)
			delLocs = append(delLocs, loadLoc(v))
			continue
		}
		remainChunks++
		sphps := splitSphs(v[6:])
		if g.keepAllSphps(sphps) {
			continue
		}
		newv := make([]byte, 6, len(v))
		copy(newv, v)
		for _, sphp := range sphps {
			if _, ok := g.keepSphps[sphp]; ok {
				newv = append(newv, sphp[:]...)
			}
		}
		rewriteChunks = append(rewriteChunks, rewriteChunk{d: d, v: newv})
	}

	log.Printf("gc: will delete:")
	log.Printf("gc:   %d images / %d manifests", len(delImages), len(delManifests))
	log.Printf("gc:   %d chunks", len(delChunks))
	log.Printf("gc:   %d bytes", delBlocks<<s.blockShift)
	log.Printf("gc: remaining:")
	log.Printf("gc:   %d images", len(g.keepImage))
	log.Printf("gc:   %d chunks (%d)", len(g.keepDig), remainChunks)
	log.Printf("gc:   %d bytes", remainBlocks<<s.blockShift)
	log.Printf("gc: rewrite %d chunks", len(rewriteChunks))

	// dry run
	if r.DryRun {
		return resp, tx.Rollback()
	}

	// actually do deletes
	// FIXME

	// sort locs before punching
	slices.SortFunc(delLocs, func(a, b erofs.SlabLoc) int {
		if a.SlabId < b.SlabId {
			return -1
		} else if a.SlabId > b.SlabId {
			return 1
		} else if a.Addr < b.Addr {
			return -1
		} else if a.Addr > b.Addr {
			return 1
		} else {
			return 0
		}
	})

	// TODO: catalog buckets

	// actually punch holes
	// FIXME: oh crap, how do we know the size?

	return resp, tx.Commit()
}

func (s *Server) gcTraceImage(g *gcCtx, sphStr string, img *pb.DbImage) error {
	sph, _, err := ParseSph(sphStr)
	if err != nil {
		return err
	}
	sphPrefix := SphPrefixFromBytes(sph[:sphPrefixBytes])

	if g.GcByState[img.MountState] {
		g.DeletedByState[img.MountState]++
		return nil
	}

	g.keepImage[sphStr] = struct{}{}
	g.keepSphps[sphPrefix] = struct{}{}
	g.RemainingByState[img.MountState]++

	m, mdigs, err := s.getManifestLocal(g.tx, sphStr)
	if err != nil {
		return err
	}

	for _, mdig := range mdigs {
		g.keepDig[mdig] = struct{}{}
	}
	for _, e := range m.Entries {
		for _, d := range cdig.FromSliceAlias(e.Digests) {
			g.keepDig[d] = struct{}{}
		}
	}

	return nil
}

func (g *gcCtx) keepAllSphps(sphps []SphPrefix) bool {
	for _, sphp := range sphps {
		if _, ok := g.keepSphps[sphp]; !ok {
			return false
		}
	}
	return true
}
