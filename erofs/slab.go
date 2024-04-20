package erofs

import (
	"context"
	"fmt"

	"github.com/dnr/styx/common"
)

type (
	SlabLoc struct {
		SlabId uint16
		Addr   uint32
	}

	SlabManager interface {
		VerifyParams(hashBytes int, blockShift, chunkShift common.BlkShift) error
		AllocateBatch(ctx context.Context, blocks []uint16, digests []byte) ([]SlabLoc, error)
		SlabInfo(slabId uint16) (tag string, totalBlocks uint32)
	}

	dummySlabManager uint32
)

func NewDummySlabManager() *dummySlabManager {
	d := dummySlabManager(0x1234)
	return &d
}

func (d *dummySlabManager) VerifyParams(hashBytes, blockShift, chunkShift int) error {
	return nil
}

func (d *dummySlabManager) AllocateBatch(ctx context.Context, blocks []uint16, digests []byte) ([]SlabLoc, error) {
	out := make([]SlabLoc, len(blocks))
	for i := range out {
		// digest := digests[i*24 : (i+1)*24]
		// log.Printf("allocating chunk len %d, digest %q", blocks[i], digest)
		out[i].Addr = *(*uint32)(d)
		(*d)++
	}
	return out, nil
}

func (d *dummySlabManager) SlabInfo(slabId uint16) (tag string, totalBlocks uint32) {
	return fmt.Sprintf("slab-%d", slabId), 1 << (40 - 12)
}
