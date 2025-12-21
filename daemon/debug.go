package daemon

import (
	"context"
	"encoding/binary"
	"log"

	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/pb"
)

func (s *Server) handleDebugReq(ctx context.Context, r *DebugReq) (*DebugResp, error) {
	// allow this even before "initialized"

	res := &DebugResp{
		DbStats: s.db.Stats(),
	}
	return res, s.db.View(func(tx *bbolt.Tx) error {
		// meta
		var dp pb.DbParams
		_ = proto.Unmarshal(tx.Bucket(metaBucket).Get(metaParams), &dp)
		res.Params = &dp

		// stats
		res.Stats = s.stats.export()

		// images
		if r.IncludeAllImages || len(r.IncludeImages) > 0 {
			res.Images = make(map[string]DebugImage)

			doImage := func(k, v []byte) {
				if v == nil {
					return
				}
				var img pb.DbImage
				if err := proto.Unmarshal(v, &img); err != nil {
					log.Print("unmarshal error iterating images", err)
					return
				}

				m, mdigs, err := s.getManifestLocal(tx, string(k))
				if err != nil {
					log.Print("unmarshal getting manifest iterating images", err)
					return
				}
				var mchunks []string
				if r.IncludeManifests {
					for _, mdig := range mdigs {
						mchunks = append(mchunks, mdig.String())
					}
				}
				var tchunks, tblocks, pchunks, pblocks int
				for _, ent := range m.Entries {
					ent.StatsInlineData = int32(len(ent.InlineData))
					digests := cdig.FromSliceAlias(ent.Digests)
					tchunks += len(digests)
					cshift := ent.ChunkShiftDef()
					for i, dig := range digests {
						chunkSize := cshift.FileChunkSize(ent.Size, i == len(digests)-1)
						blocks := s.blockShift.Blocks(chunkSize)
						tblocks += int(blocks)
						if _, present := s.digestPresent(tx, dig); present {
							ent.StatsPresentChunks++
							ent.StatsPresentBlocks += int32(blocks)
							pchunks += 1
							pblocks += int(blocks)
						}
						ent.DebugDigests = append(ent.DebugDigests, dig.String())
					}
					ent.InlineData = nil
					ent.Digests = nil
				}
				if !r.IncludeManifests {
					m = nil
				}

				res.Images[img.StorePath] = DebugImage{
					Image:          &img,
					Manifest:       m,
					ManifestChunks: mchunks,
					Stats: DebugSizeStats{
						TotalChunks:   tchunks,
						TotalBlocks:   tblocks,
						PresentChunks: pchunks,
						PresentBlocks: pblocks,
					},
				}
				img.StorePath = ""
			}

			if r.IncludeAllImages {
				cur := tx.Bucket(imageBucket).Cursor()
				for k, v := cur.First(); k != nil; k, v = cur.Next() {
					doImage(k, v)
				}
			} else {
				for _, img := range r.IncludeImages {
					doImage([]byte(img), tx.Bucket(imageBucket).Get([]byte(img)))
				}
			}
		}

		// slabs
		if r.IncludeSlabs {
			slabroot := tx.Bucket(slabBucket)
			cur := slabroot.Cursor()
			for k, _ := cur.First(); k != nil; k, _ = cur.Next() {
				blockSizes := make(map[uint32]uint32)
				sb := slabroot.Bucket(k)
				si := DebugSlabInfo{
					Index:         binary.BigEndian.Uint16(k),
					ChunkSizeDist: make(map[uint32]int),
				}
				scur := sb.Cursor()
				for sk, _ := scur.First(); sk != nil; {
					nextSk, _ := scur.Next()
					addr := addrFromKey(sk)
					if addr&presentMask == 0 {
						var nextAddr uint32
						if nextSk != nil && nextSk[0]&0x80 == 0 {
							nextAddr = addrFromKey(nextSk)
						} else {
							nextAddr = common.TruncU32(sb.Sequence())
						}
						blockSize := uint32(nextAddr - addr)
						blockSizes[addr] = blockSize
						si.Stats.TotalChunks++
						si.Stats.TotalBlocks += int(blockSize)
						si.ChunkSizeDist[blockSize]++
					} else {
						si.Stats.PresentChunks++
						si.Stats.PresentBlocks += int(blockSizes[addr&^presentMask])
					}
					sk = nextSk
				}
				res.Slabs = append(res.Slabs, &si)
			}
		}

		// chunks
		if r.IncludeAllChunks || len(r.IncludeChunks) > 0 {
			slabroot := tx.Bucket(slabBucket)
			cb := tx.Bucket(chunkBucket)
			res.Chunks = make(map[string]*DebugChunkInfo)

			doChunk := func(k, v []byte) {
				if len(k) < cdig.Bytes {
					log.Println("too short chunk key in bucket", k)
					return
				}
				var ci DebugChunkInfo
				loc := loadLoc(v)
				ci.Slab, ci.Addr = loc.SlabId, loc.Addr
				for _, sphp := range sphpsFromLoc(v) {
					sph, name := s.catalogFindName(tx, sphp)
					ci.StorePaths = append(ci.StorePaths, sph.String()+"-"+name)
				}
				ci.Present = slabroot.Bucket(slabKey(ci.Slab)).Get(addrKey(ci.Addr|presentMask)) != nil
				res.Chunks[cdig.FromBytes(k).String()] = &ci
			}

			if r.IncludeAllChunks {
				cur := cb.Cursor()
				for k, v := cur.First(); k != nil; k, v = cur.Next() {
					doChunk(k, v)
				}
			} else {
				for _, cstr := range r.IncludeChunks {
					dig, err := cdig.FromBase64(cstr)
					if err != nil {
						log.Println("debug chunk parse error", cstr, err)
					} else if v := cb.Get(dig[:]); v == nil {
						log.Println("debug chunk missing", cstr)
					} else {
						doChunk(dig[:], v)
					}
				}
			}
		}

		// chunk sharing
		if r.IncludeChunkSharing {
			m := make(map[int]int)
			cur := tx.Bucket(chunkBucket).Cursor()
			for k, v := cur.First(); k != nil; k, v = cur.Next() {
				m[(len(v)-6)/sphPrefixBytes]++
			}
			res.ChunkSharingDist = m
		}

		return nil
	})
}
