package daemon

import "github.com/dnr/styx/pb"

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
		IncludeImages bool
		IncludeChunks bool
	}
	DebugResp struct {
		Params *pb.GlobalParams
		Stats  Stats
		Images map[string]*pb.DbImage
		Slabs  []*DebugSlabInfo
		Chunks map[string]*DebugChunkInfo
		// TODO: add manifests
		// TODO: add boltdb stats
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

	Status struct {
		Success bool
		Error   string
	}
)
