package daemon

var (
	// protocol is json over http over unix socket
	// socket is path.Join(CachePath, Socket)
	// accessible to root only!
	Socket     = "styx.sock"
	MountPath  = "/mount"
	UmountPath = "/umount"
	GcPath     = "/gc"
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

	GcReq struct {
	}

	Status struct {
		Success bool
		Error   string
	}
	genericResp struct {
		Status
	}
)
