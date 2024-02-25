package erofs

import "fmt"

type (
	SlabLoc struct {
		SlabId uint16
		Addr   uint32
	}

	SlabManager interface {
		AllocateBatch(digests []byte, hashBytes int) ([]SlabLoc, error)
		SlabInfo(slabId uint16) (tag string, totalBlocks uint32)
	}

	dummySlabManager uint32
)

func NewDummySlabManager() *dummySlabManager {
	d := dummySlabManager(0)
	return &d
}

func (d *dummySlabManager) AllocateBatch(digests []byte, hashBytes int) ([]SlabLoc, error) {
	n := len(digests) / hashBytes
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
