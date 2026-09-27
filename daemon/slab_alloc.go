package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/erofs"
	"go.etcd.io/bbolt"
)

// reserve blocks in the first slab with space in [slabId, slabEnd)
func (s *Server) allocateSlabSpace(tx *bbolt.Tx, slabId, slabEnd uint16, blocks uint32) (erofs.SlabLoc, error) {
	limit := uint64(slabBytes)>>s.blockShift - reservedBlocks
	if blocks == 0 || uint64(blocks) >= limit-reservedBlocks {
		return erofs.SlabLoc{}, fmt.Errorf("invalid slab allocation size: %d blocks", blocks)
	}
	slabroot := tx.Bucket(slabBucket)
	for {
		if sb, err := slabroot.CreateBucketIfNotExists(slabKey(slabId)); err != nil {
			return erofs.SlabLoc{}, err
		} else if seq := max(sb.Sequence(), reservedBlocks); seq+uint64(blocks) >= limit {
			if slabId++; slabId >= slabEnd {
				return erofs.SlabLoc{}, errors.New("no slabs left")
			}
			continue
		} else if err := sb.SetSequence(seq + uint64(blocks)); err != nil {
			return erofs.SlabLoc{}, err
		} else {
			return erofs.SlabLoc{SlabId: slabId, Addr: common.TruncU32(seq)}, nil
		}
	}
}

func (s *Server) allocateImageSpace(imgBlocks uint32) (loc erofs.SlabLoc, retErr error) {
	retErr = s.db.Update(func(tx *bbolt.Tx) error {
		var err error
		loc, err = s.allocateSlabSpace(tx, imageSlabOffset, math.MaxUint16, imgBlocks)
		return err
	})
	if retErr == nil {
		retErr = s.setupSlab(loc.SlabId)
	}
	return
}

// implement erofs.SlabManager interface
func (s *Server) VerifyParams(blockShift shift.Shift) error {
	if blockShift != s.blockShift {
		return errors.New("mismatched params")
	}
	return nil
}

// implement erofs.SlabManager interface
func (s *Server) AllocateBatch(ctx context.Context, blocks []uint16, digests []cdig.CDig) ([]erofs.SlabLoc, error) {
	sph, forManifest, ok := fromAllocateCtx(ctx)
	if !ok {
		return nil, errors.New("missing allocate context")
	}

	n := len(blocks)
	if n != len(digests) {
		return nil, errors.New("mismatched lengths")
	}
	out := make([]erofs.SlabLoc, n)
	err := s.db.Update(func(tx *bbolt.Tx) error {
		cb, slabroot := tx.Bucket(chunkBucket), tx.Bucket(slabBucket)
		slabId, slabEnd := uint16(0), uint16(manifestSlabOffset)
		if forManifest {
			slabId, slabEnd = manifestSlabOffset, imageSlabOffset
		}

		for i := range out {
			digest := digests[i][:]
			if loc := cb.Get(digest); loc == nil {
				loc, err := s.allocateSlabSpace(tx, slabId, slabEnd, uint32(blocks[i]))
				if err != nil {
					return err
				}
				slabId = loc.SlabId // start here next time
				sb := slabroot.Bucket(slabKey(slabId))
				if err := cb.Put(digest, locValue(loc.SlabId, loc.Addr, blocks[i], sph)); err != nil {
					return err
				} else if err = sb.Put(addrKey(loc.Addr), slabValue(blocks[i], digests[i])); err != nil {
					return err
				}
				out[i] = loc
			} else {
				if newLoc := appendSph(loc, sph); newLoc != nil {
					if err := cb.Put(digest, newLoc); err != nil {
						return err
					}
				}
				out[i] = loadLoc(loc)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return common.ValOrErr(out, s.setupAllocatedSlabs(out))
}

// implement erofs.SlabManager interface
func (s *Server) SlabInfo(slabId uint16) (tag string, totalBlocks uint32) {
	return s.slabDmName(slabId), common.TruncU32(uint64(slabBytes) >> s.blockShift)
}

// like AllocateBatch but only lookup
func (s *Server) lookupLocs(tx *bbolt.Tx, digests []cdig.CDig) ([]erofs.SlabLoc, error) {
	out := make([]erofs.SlabLoc, len(digests))
	cb := tx.Bucket(chunkBucket)
	for i := range out {
		loc := cb.Get(digests[i][:])
		if loc == nil {
			return nil, fmt.Errorf("missing chunk %s in lookupLocs", digests[i])
		}
		out[i] = loadLoc(loc)
	}
	return out, nil
}
