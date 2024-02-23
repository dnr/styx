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
		StorePathHash string
		MountPoint    string
	}
	MountResp struct {
	}

	UmountReq struct {
		MountPoint string
	}
	UmountResp struct {
	}

	DeleteReq struct {
		StorePathHash string
	}
	DeleteResp struct {
	}
)
