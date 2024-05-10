package daemon

import (
	"go.etcd.io/bbolt"

	"github.com/dnr/styx/pb"
)

var (
	// protocol is json over http over unix socket
	// socket is path.Join(CachePath, Socket)
	// accessible to root only!
	Socket     = "styx.sock"
	MountPath  = "/mount"
	UmountPath = "/umount"
	GcPath     = "/gc"
	DebugPath  = "/debug"
)

type (
	MountReq struct {
		Upstream   string
		StorePath  string
		MountPoint string
		NarSize    int64 // optional
	}
	// returns Status

	UmountReq struct {
		StorePath string
	}
	// returns Status

	GcReq struct {
	}
	// returns Status

	DebugReq struct {
		IncludeAllImages bool
		IncludeImages    []string // list of base32 sph

		IncludeSlabs  bool
		IncludeChunks bool
	}
	DebugResp struct {
		Params  *pb.GlobalParams
		Stats   Stats
		DbStats bbolt.Stats
		Images  map[string]DebugImage
		Slabs   []*DebugSlabInfo
		Chunks  map[string]*DebugChunkInfo
	}
	DebugSlabInfo struct {
		Index         uint16
		TotalChunks   int
		TotalBlocks   int
		PresentChunks int
		PresentBlocks int
		ChunkSizeDist map[uint32]int
	}
	DebugChunkInfo struct {
		Slab       uint16
		Addr       uint32
		StorePaths []string
		Present    bool
	}
	DebugImage struct {
		Image    *pb.DbImage
		Manifest *pb.Manifest
	}

	Status struct {
		Success bool
		Error   string
	}
)
