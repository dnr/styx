package daemon

import (
	"go.etcd.io/bbolt"

	"github.com/dnr/styx/pb"
)

var (
	// protocol is json over http over unix socket
	// socket is path.Join(CachePath, Socket)
	// accessible to root only!
	Socket          = "styx.sock"
	InitPath        = "/init"
	MountPath       = "/mount"
	UmountPath      = "/umount"
	MaterializePath = "/materialize"
	VaporizePath    = "/vaporize"
	PrefetchPath    = "/prefetch"
	GcPath          = "/gc"
	DebugPath       = "/debug"
	RepairPath      = "/repair"
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
		NarSize    int64 `json:",omitempty"` // optional
	}
	// returns Status

	UmountReq struct {
		StorePath string
	}
	// returns Status

	MaterializeReq struct {
		Upstream  string
		StorePath string
		DestPath  string
		NarSize   int64 `json:",omitempty"` // optional
	}
	// returns Status

	VaporizeReq struct {
		Path string
		Name string
	}
	// returns Status

	PrefetchReq struct {
		// absolute path of file or directory to prefetch (unless using StorePath)
		Path string
		// optional, if set use this StorePath and consider Path under it
		StorePath string
	}
	// returns Status

	GcReq struct {
	}
	// returns Status

	RepairReq struct {
		Presence bool `json:",omitempty"`
	}
	// returns Status

	DebugReq struct {
		IncludeAllImages bool     `json:",omitempty"`
		IncludeImages    []string `json:",omitempty"` // list of base32 sph

		IncludeSlabs  bool `json:",omitempty"`
		IncludeChunks bool `json:",omitempty"`
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
		Success bool   `json:",omitempty"`
		Error   string `json:",omitempty"`
	}
)
