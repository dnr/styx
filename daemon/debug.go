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

func (s *server) handleDebugReq(ctx context.Context, r *DebugReq) (*DebugResp, error) {
	res := &DebugResp{
		DbStats: s.db.Stats(),
	}
	return res, s.db.View(func(tx *bbolt.Tx) error {
		// meta
		var gp pb.GlobalParams
		_ = proto.Unmarshal(tx.Bucket(metaBucket).Get(metaParams), &gp)
		res.Params = &gp

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

				m, err := s.getManifestLocal(ctx, tx, k)
				if err != nil {
					log.Print("unmarshal getting manifest iterating images", err)
					return
				}
				for _, ent := range m.Entries {
					ent.StatsInlineData = int32(len(ent.InlineData))
					digests := cdig.FromSliceAlias(ent.Digests)
					for i := range digests {
						if _, present := s.digestPresent(tx, digests[i]); present {
							ent.StatsPresentChunks++
							chunkSize := common.ChunkShift.FileChunkSize(ent.Size, i == len(digests)-1)
							blocks := s.blockShift.Blocks(chunkSize)
							ent.StatsPresentBlocks += int32(blocks)
						}
					}
					ent.InlineData = nil
					ent.Digests = nil
				}

				res.Images[img.StorePath] = DebugImage{Image: &img, Manifest: m}
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
						si.TotalChunks++
						si.TotalBlocks += int(blockSize)
						si.ChunkSizeDist[blockSize]++
					} else {
						si.PresentChunks++
						si.PresentBlocks += int(blockSizes[addr&^presentMask])
					}
					sk = nextSk
				}
				res.Slabs = append(res.Slabs, &si)
			}
		}

		// chunks
		if r.IncludeChunks {
			slabroot := tx.Bucket(slabBucket)
			res.Chunks = make(map[string]*DebugChunkInfo)
			cur := tx.Bucket(chunkBucket).Cursor()
			for k, v := cur.First(); k != nil; k, v = cur.Next() {
				if len(k) < cdig.Bytes {
					continue
				}
				var ci DebugChunkInfo
				loc := loadLoc(v)
				ci.Slab, ci.Addr = loc.SlabId, loc.Addr
				for _, sphp := range splitSphs(v[6:]) {
					sph, name := s.catalogFindName(tx, sphp)
					ci.StorePaths = append(ci.StorePaths, sph.String()+"-"+name)
				}
				ci.Present = slabroot.Bucket(slabKey(ci.Slab)).Get(addrKey(ci.Addr|presentMask)) != nil
				res.Chunks[cdig.FromBytes(k).String()] = &ci
			}
		}
		return nil
	})
}
