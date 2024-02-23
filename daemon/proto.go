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
		StoreHash  string
		MountPoint string
	}
	MountResp struct {
	}

	UmountReq struct {
		MountPoint string
	}
	UmountResp struct {
	}

	DeleteReq struct {
		StoreHash string
	}
	DeleteResp struct {
	}
)
