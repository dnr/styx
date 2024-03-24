package daemon

var (
	// protocol is json over http over unix socket
	// socket is path.Join(CachePath, Socket)
	// accessible to root only!
	Socket     = "styx.sock"
	QueryPath  = "/query"
	MountPath  = "/mount"
	UmountPath = "/umount"
	DeletePath = "/delete"
)

type (
	QueryReq struct {
		StorePath string // hash plus name, e.g. "m28r6mf37cc8bwwq52kqdzdkc9yrq3ag-nix-2.18.1"
	}
	QueryResp struct {
		Status

		MountState string
		NarSize    int64 // nar size from narinfo
	}

	MountReq struct {
		StorePath  string
		MountPoint string
	}

	UmountReq struct {
		StorePath string
	}

	DeleteReq struct {
		StorePath string
	}

	Status struct {
		Success bool
		Error   string
	}
	genericResp struct {
		Status
	}
)
