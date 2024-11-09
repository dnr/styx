package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
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
	locAndSize struct {
		erofs.SlabLoc
		blocks uint32
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
	var delImages, delCatalogF, delCatalogR [][]byte
	for k, v := ibcur.First(); k != nil; k, v = ibcur.Next() {
		if _, ok := g.keepImage[string(k)]; ok {
			continue
		}
		delImages = append(delImages, k)
		var img pb.DbImage
		var sph Sph
		var err error
		var spName string
		if proto.Unmarshal(v, &img) != nil {
			continue
		} else if sph, _, err = ParseSph(img.StorePath); err != nil {
			continue
		} else if _, spName, _ = strings.Cut(img.StorePath, "-"); spName == "" {
			continue
		}
		fkey := bytes.Join([][]byte{[]byte(spName), []byte{0}, sph[:]}, nil)
		rkey := sph[:]
		manifestSph := makeManifestSph(sph)
		mfkey := bytes.Join([][]byte{[]byte(isManifestPrefix), []byte(spName), []byte{0}, manifestSph[:]}, nil)
		mrkey := manifestSph[:]
		delCatalogF = append(delCatalogF, fkey, mfkey)
		delCatalogR = append(delCatalogR, rkey, mrkey)
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
	// var delBlocks, remainBlocks int64
	var remainChunks int64
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
	// log.Printf("gc:   %d bytes", delBlocks<<s.blockShift)
	log.Printf("gc: remaining:")
	log.Printf("gc:   %d images", len(g.keepImage))
	log.Printf("gc:   %d chunks (%d)", len(g.keepDig), remainChunks)
	// log.Printf("gc:   %d bytes", remainBlocks<<s.blockShift)
	log.Printf("gc: rewrite %d chunks", len(rewriteChunks))

	// dry run
	if r.DryRun {
		return resp, tx.Rollback()
	}

	// actually do deletes

	// images
	for _, k := range delImages {
		g.ib.Delete(k)
	}
	// manifests
	for _, k := range delManifests {
		g.mb.Delete(k)
	}

	// chunks delete
	for _, d := range delChunks {
		g.cb.Delete(d[:])
	}

	// chunks rewrite
	for _, rew := range rewriteChunks {
		g.cb.Put(rew.d[:], rew.v)
	}

	// catalog
	cfb := tx.Bucket(catalogFBucket)
	for _, dcf := range delCatalogF {
		cfb.Delete(dcf)
	}
	crb := tx.Bucket(catalogRBucket)
	for _, dcr := range delCatalogR {
		crb.Delete(dcr)
	}

	// locs

	// sort for locality and coalescing gaps
	slices.SortFunc(delLocs, locCmp)

	sb := tx.Bucket(slabBucket)
	var lastBucket *bbolt.Bucket
	var lastKey uint16 = math.MaxUint16
	for _, l := range delLocs {
		lsb := lastBucket
		if l.SlabId != lastKey {
			lsb = sb.Bucket(slabKey(l.SlabId))
			lastBucket, lastKey = lsb, l.SlabId
		}
		if lsb != nil {
			lsb.Delete(addrKey(l.Addr))
			lsb.Delete(addrKey(l.Addr | presentMask))
		}
	}

	// after all locs have been deleted, find ranges to punch out
	var punchLocs []locAndSize
	for _, l := range delLocs {
		lsb := lastBucket
		if l.SlabId != lastKey {
			lsb = sb.Bucket(slabKey(l.SlabId))
			lastBucket, lastKey = lsb, l.SlabId
		}
		var end uint32
		if k, _ := lsb.Cursor().Seek(addrKey(l.Addr)); k == nil {
			end = common.TruncU32(lsb.Sequence()) // end of slab
		} else {
			end = loadLoc(k).Addr
			if end&presentMask != 0 {
				end = common.TruncU32(lsb.Sequence())
			}
		}
		if len(punchLocs) > 0 {
			last := punchLocs[len(punchLocs)-1]
			lastEnd := last.Addr + last.blocks
			if end == lastEnd {
				continue
			} else if end < lastEnd {
				return nil, errors.New("this shouldn't happen")
			}
		}
		punchLocs = append(punchLocs, locAndSize{
			SlabLoc: l,
			blocks:  end - l.Addr,
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// actually punch holes
	s.stateLock.Lock()
	readFds := maps.Clone(s.readfdBySlab)
	s.stateLock.Unlock()
	for _, ls := range punchLocs {
		if cfd := readFds[ls.SlabId].cacheFd; cfd > 0 {
			err := unix.Fallocate(
				cfd,
				unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE,
				int64(ls.Addr)<<s.blockShift,
				int64(ls.blocks)<<s.blockShift,
			)
			if err != nil {
				log.Print("fallocate punch error (slab %d as fd %d, %d @ %d): %s",
					ls.SlabId, cfd, ls.blocks, ls.Addr, err,
				)
			}
		}
	}

	return resp, nil
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

func locCmp(a, b erofs.SlabLoc) int {
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
}
