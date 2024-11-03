package daemon

import (
	"context"
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

		keepImage map[string]struct{} // sph string // FIXME: needed?
		keepDig   map[cdig.CDig]struct{}
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
		keepDig:   make(map[cdig.CDig]struct{}, 100000),
	}

	// use image bucket as roots
	ibcur := g.ib.Cursor()
	for k, v := ibcur.First(); k != nil; k, v = ibcur.Next() {
		var img pb.DbImage
		if proto.Unmarshal(v, &img) == nil {
			s.gcTraceImage(g, string(v), &img)
		}
	}

	// find images to delete
	var delImages [][]byte
	for k, _ := ibcur.First(); k != nil; k, _ = ibcur.Next() {
		if _, ok := s.keepImage(string(k)); ok {
			continue
		}
		delImages = append(delImages, k)
	}

	// find manifests to delete
	var delManifests [][]byte
	for k, _ := mbcur.First(); k != nil; k, _ = mbcur.Next() {
		if _, ok := s.keepImage(string(k)); ok {
			continue
		}
		delManifests = append(delManifests, k)
	}

	// find all chunks to delete
	cbcur := g.cb.Cursor()
	var delChunks []cdig.CDig
	var delLocs []erofs.SlabLoc
	for k, v := cbcur.First(); k != nil; k, v = cbcur.Next() {
		d := cdig.FromBytes(k)
		if _, ok := g.keepDig[d]; ok {
			continue
		}
		delChunks = append(delChunks, d)
		delLocs = append(delLocs, loadLoc(v))
	}

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
	if g.GcByState[img.MountState] {
		g.DeletedByState[img.MountState]++
		return nil
	}

	g.keepImage[sphStr] = struct{}{}
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
