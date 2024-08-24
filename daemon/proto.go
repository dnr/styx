package daemon

import (
	"go.etcd.io/bbolt"

	"github.com/dnr/styx/pb"
)

var (
	// protocol is json over http over unix socket
	// socket is path.Join(CachePath, Socket)
	// accessible to root only!
	Socket       = "styx.sock"
	InitPath     = "/init"
	MountPath    = "/mount"
	UmountPath   = "/umount"
	PrefetchPath = "/prefetch"
	GcPath       = "/gc"
	DebugPath    = "/debug"
)

type (
	InitReq struct {
		PubKeys []string
		Params  pb.DaemonParams
	}
	// returns Status

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

	PrefetchReq struct {
		Path string
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
		Params  *pb.DbParams
		Stats   Stats
		DbStats bbolt.Stats
		Images  map[string]DebugImage      `json:",omitempty"`
		Slabs   []*DebugSlabInfo           `json:",omitempty"`
		Chunks  map[string]*DebugChunkInfo `json:",omitempty"`
	}
	DebugSizeStats struct {
		TotalChunks   int
		TotalBlocks   int
		PresentChunks int
		PresentBlocks int
	}
	DebugSlabInfo struct {
		Index         uint16
		Stats         DebugSizeStats
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
		Stats    DebugSizeStats
	}

	Status struct {
		Success bool
		Error   string
	}
)
