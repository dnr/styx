package erofs

import (
	"context"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
)

type (
	SlabLoc struct {
		SlabId uint16
		Addr   uint32
	}

	SlabManager interface {
		VerifyParams(blockShift common.BlkShift) error
		AllocateBatch(ctx context.Context, blocks []uint16, digests []cdig.CDig) ([]SlabLoc, error)
		SlabInfo(slabId uint16) (tag string, totalBlocks uint32)
	}
)
