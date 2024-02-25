package erofs

import "fmt"

type (
	SlabLoc struct {
		SlabId uint16
		Addr   uint32
	}

	SlabManager interface {
		VerifyParams(hashBytes, blockSize, chunkSize int) error
		AllocateBatch(blocks []uint16, digests []byte) ([]SlabLoc, error)
		SlabInfo(slabId uint16) (tag string, totalBlocks uint32)
	}

	dummySlabManager uint32
)

func NewDummySlabManager() *dummySlabManager {
	d := dummySlabManager(0)
	return &d
}

func (d *dummySlabManager) VerifyParams(hashBytes, blockSize, chunkSize int) error {
	return nil
}

func (d *dummySlabManager) AllocateBatch(blocks []uint16, digests []byte) ([]SlabLoc, error) {
	n := len(blocks)
	out := make([]SlabLoc, n)
	for i := range out {
		out[i].Addr = *(*uint32)(d)
		(*d)++
	}
	return out, nil
}

func (d *dummySlabManager) SlabInfo(slabId uint16) (tag string, totalBlocks uint32) {
	return fmt.Sprintf("slab-%d", slabId), 1 << (40 - 12)
}
