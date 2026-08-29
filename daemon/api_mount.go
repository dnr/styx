package daemon

import (
	"context"
	"net/http"
	"strings"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/pb"
	"golang.org/x/sys/unix"
)

func (s *Server) handleMountReq(ctx context.Context, r *MountReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	} else if !s.ondemand() {
		return nil, mwErr(http.StatusPreconditionFailed, "styx on-demand features disabled")
	}
	_, sphStr, _, err := ParseSphAndName(r.StorePath)
	if err != nil {
		return nil, err
	} else if r.Upstream == "" {
		return nil, mwErr(http.StatusBadRequest, "invalid upstream")
	} else if !strings.HasPrefix(r.MountPoint, "/") {
		return nil, mwErr(http.StatusBadRequest, "mount point must be absolute path")
	}

	common.NormalizeUpstream(&r.Upstream)

	err = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if img.MountState == pb.MountState_Mounted {
			if img.MountPoint == r.MountPoint {
				// nix thinks it's not mounted but it is. return success so nix can enter in db.
				return errAlreadyMounted
			} else {
				return errAlreadyMountedElsewhere
			}
		}
		img.StorePath = r.StorePath
		img.Upstream = r.Upstream
		img.MountState = pb.MountState_Requested
		img.MountPoint = r.MountPoint
		img.LastMountError = ""
		img.NarSize = r.NarSize
		return nil
	})
	if err != nil {
		if err == errAlreadyMounted {
			err = nil
		}
		return nil, err
	}

	return nil, s.tryMount(ctx, r)
}

func (s *Server) handleUmountReq(ctx context.Context, r *UmountReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	} else if !s.ondemand() {
		return nil, mwErr(http.StatusPreconditionFailed, "styx on-demand features disabled")
	}

	// allowed to leave out the name part here
	_, sphStr, err := ParseSph(r.StorePath)
	if err != nil {
		return nil, err
	}

	var mp string
	err = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if img.MountState != pb.MountState_Mounted && img.MountState != pb.MountState_UnmountRequested {
			// TODO: check if erofs is actually mounted anyway and unmount
			return mwErr(http.StatusNotFound, "not mounted")
		} else if mp = img.MountPoint; mp == "" {
			return mwErr(http.StatusInternalServerError, "mount point not set")
		}
		img.MountState = pb.MountState_UnmountRequested
		return nil
	})
	if err != nil {
		return nil, err
	}

	umountErr := unix.Unmount(mp, unix.MNT_DETACH)

	if umountErr == nil {
		_ = s.imageTx(sphStr, func(img *pb.DbImage) error {
			img.MountState = pb.MountState_Unmounted
			img.MountPoint = ""
			return nil
		})
	}

	return nil, umountErr
}
