package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/patched/loopback"
	nbdclient "github.com/pojntfx/go-nbd/pkg/client"
	"go.etcd.io/bbolt"
)

const slabBytes = 1 << 40

func (s *Server) slabPath(tp string, slabId uint16) string {
	return filepath.Join(s.CachePath, "slabs", fmt.Sprintf("slab%d%s", slabId, tp))
}

func (s *Server) setupSlab(slabId uint16) err {
	// setup loopback for metadata
	metaName := s.slabPath("meta", slabId)
	metaFile := os.OpenFile(metaName, os.O_RDWR, 0o600)
	metaDev, err := loopback.NextLoopDevice()
	err := loopback.Loop(metaDev, metaFile)

	// setup loopback for data file
	dataName := s.slabPath("data", slabId)
	dataFile := os.OpenFile(metaName, os.O_RDWR, 0o600)
	dataDev, err := loopback.NextLoopDevice()
	err := loopback.Loop(dataDev, dataFile)

	// setup nbd
	addr := s.nbdsock.Load().(net.Listener).Addr()
	nbdConn := net.Dial(addr.Network(), addr.String())
	// TODO: fix race between find and connect (use netlink)
	nbdName, err := findFreeNbdDev()
	nbdDev, err := os.OpenFile(nbdName, os.O_RDWR, 0o600)
	nbdclient.Connect(nbdConn, nbdDev, &nbdclient.Options{
		ExportName: fmt.Sprintf("slab%d", slabId),
	})

	// setup dm-clone
	// FIXME
}

// erofs.SlabManager interface
func (s *Server) VerifyParams(blockShift shift.Shift) error {
	if blockShift != s.blockShift {
		return errors.New("mismatched params")
	}
	return nil
}

// erofs.SlabManager interface
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
		var slabId uint16 = 0
		if forManifest {
			slabId = manifestSlabOffset
		}
		sb, err := slabroot.CreateBucketIfNotExists(slabKey(slabId))
		if err != nil {
			return err
		}
		// reserve some blocks for future purposes
		seq := max(sb.Sequence(), reservedBlocks)

		for i := range out {
			digest := digests[i][:]
			if loc := cb.Get(digest); loc == nil {
				// allocate
				if seq >= slabBytes>>s.blockShift {
					slabId++
					if sb, err = slabroot.CreateBucketIfNotExists(slabKey(slabId)); err != nil {
						return err
					}
					seq = max(sb.Sequence(), reservedBlocks)
				}
				addr := common.TruncU32(seq)
				seq += uint64(blocks[i])
				if err := cb.Put(digest, locValue(slabId, addr, sph)); err != nil {
					return err
				} else if err = sb.Put(addrKey(addr), digest); err != nil {
					return err
				}
				out[i] = erofs.SlabLoc{slabId, addr}
			} else {
				if newLoc := appendSph(loc, sph); newLoc != nil {
					if err := cb.Put(digest, newLoc); err != nil {
						return err
					}
				}
				out[i] = loadLoc(loc)
			}
		}

		return sb.SetSequence(seq)
	})
	return common.ValOrErr(out, err)
}

// erofs.SlabManager interface
func (s *Server) SlabInfo(slabId uint16) (tag string, totalBlocks uint32) {
	// len(tag) < 64
	return s.slabPath("clone", slabId), common.TruncU32(uint64(slabBytes) >> s.blockShift)
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
