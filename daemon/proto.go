package daemon

var (
	// protocol is json over http over unix socket
	// socket is path.Join(CachePath, Socket)
	// accessible to root only!
	Socket     = "styx.sock"
	MountPath  = "/mount"
	UmountPath = "/umount"
	DeletePath = "/delete"
)

type (
	MountReq struct {
		Upstream   string
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
