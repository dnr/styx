package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/anatol/devmapper.go"
	"github.com/dnr/styx/common"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

// on-demand mount management

func (s *Server) tryMount(ctx context.Context, req *MountReq) error {
	_, sphStr, _ := ParseSph(req.StorePath)

	var imgLoc erofs.SlabLoc
	var imgBlocks uint32

	err := s.db.View(func(tx *bbolt.Tx) error {
		var img pb.DbImage
		if buf := tx.Bucket(imageBucket).Get([]byte(sphStr)); buf == nil {
			return nil
		} else if err := proto.Unmarshal(buf, &img); err != nil {
			return err
		}
		imgLoc = erofs.SlabLoc{
			SlabId: common.TruncU16(img.ImageSlabId),
			Addr:   common.TruncU32(img.ImageBlockStart),
		}
		imgBlocks = common.TruncU32(img.ImageBlockLength)
		return nil
	})
	if err != nil {
		return err
	}

	var imagePrefix []byte
	if imgLoc.Addr > 0 && imgBlocks > 0 {
		// we have it already, read first block out of the image slab
		if err := s.setupSlab(imgLoc.SlabId); err != nil {
			return err
		}
		imagePrefix = make([]byte, 4096)
		n, err := unix.Pread(s.getReadFd(imgLoc.SlabId), imagePrefix, int64(imgLoc.Addr)<<s.blockShift)
		if err != nil {
			return err
		}
		if n != len(imagePrefix) {
			return io.ErrUnexpectedEOF
		}
	} else {
		// if no image yet, get the manifest and build it
		_, image, err := s.getManifestAndBuildImage(ctx, req)
		if err != nil {
			return err
		}
		imgBytes := int64(len(image))
		if s.blockShift.Leftover(imgBytes) > 0 {
			return errors.New("image is not multiple of block size")
		}
		imgBlocks = uint32(s.blockShift.Blocks(imgBytes))
		// allocate and write to image slab
		imgLoc, err = s.allocateImageSpace(imgBlocks)
		if err != nil {
			return err
		} else if n, err := unix.Pwrite(s.getWriteFd(imgLoc.SlabId), image, int64(imgLoc.Addr)<<s.blockShift); err != nil {
			return err
		} else if n != len(image) {
			return io.ErrShortWrite
		}
		// need to sync before this shows up in the dm-linear device (also good to do before we
		// record the image has been written in the db).
		if err = unix.Fdatasync(s.getWriteFd(imgLoc.SlabId)); err != nil {
			return err
		}
		err = s.imageTx(sphStr, func(img *pb.DbImage) error {
			img.ImageSlabId = uint32(imgLoc.SlabId)
			img.ImageBlockStart = int64(imgLoc.Addr)
			img.ImageBlockLength = int64(imgBlocks)
			return nil
		})
		if err != nil {
			return err
		}

		imagePrefix = image[:4096]
	}

	// collect device paths
	slabsUsed := erofs.SlabsUsed(imagePrefix)
	devs := make([]string, len(slabsUsed))
	for i, dmName := range slabsUsed {
		if !strings.HasPrefix(dmName, "styx-slab-") {
			return fmt.Errorf("invalid device name in image %s[%d]", sphStr, i)
		}
		dmPath, err := findDmByName(dmName)
		if err != nil {
			return fmt.Errorf("%q not found in image %s[%d]", dmName, sphStr, i)
		}
		devs[i] = "device=" + dmPath
	}
	opts := strings.Join(devs, ",")

	// set up/reuse dm linear for image
	lo, err := s.locache.findOrAttach(s.slabPath("data", imgLoc.SlabId))
	if err != nil {
		return err
	}
	dmPath, err := s.setupDm(
		"styx-image-"+sphStr,
		devmapper.ReadOnlyFlag,
		&devmapper.LinearTable{
			Start:         0,
			Length:        uint64(imgBlocks) << s.blockShift,
			BackendDevice: lo.Path(),
			BackendOffset: uint64(imgLoc.Addr) << s.blockShift,
		},
	)
	if err != nil {
		return err
	}

	// do real mount
	var mountErr error
	isBare := erofs.IsBare(imagePrefix)
	if isBare {
		// set up empty file on target mount point
		if st, err := os.Lstat(req.MountPoint); err != nil || !st.Mode().IsRegular() {
			if err = os.RemoveAll(req.MountPoint); err != nil {
				return fmt.Errorf("error clearing mount point for bare file: %w", err)
			} else if err = os.WriteFile(req.MountPoint, nil, 0o644); err != nil {
				return fmt.Errorf("error creating mount point for bare file: %w", err)
			}
		}
		// mount to private dir
		privateMp := filepath.Join(s.cfg.CachePath, "bare", sphStr)
		_ = os.MkdirAll(privateMp, 0o755)
		mountErr = unix.Mount(dmPath, privateMp, "erofs", unix.MS_RDONLY, opts)
		if mountErr == nil {
			// now bind the bare file where it should go
			mountErr = unix.Mount(privateMp+erofs.BarePath, req.MountPoint, "none", unix.MS_BIND, "")
		}
		// whether we succeeded or failed, unmount the original and clean up
		_ = unix.Unmount(privateMp, 0)
		_ = os.Remove(privateMp)
	} else {
		_ = os.MkdirAll(req.MountPoint, 0o755)
		mountErr = unix.Mount(dmPath, req.MountPoint, "erofs", unix.MS_RDONLY, opts)
	}

	_ = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if mountErr == nil {
			img.MountState = pb.MountState_Mounted
			img.LastMountError = ""
		} else {
			img.MountState = pb.MountState_MountError
			img.LastMountError = mountErr.Error()
		}
		return nil
	})

	return mountErr
}

func (s *Server) restoreMounts() {
	var toRestore []*pb.DbImage
	_ = s.db.View(func(tx *bbolt.Tx) error {
		cur := tx.Bucket(imageBucket).Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var img pb.DbImage
			if err := proto.Unmarshal(v, &img); err != nil {
				log.Print("unmarshal error iterating images", string(k), err)
				continue
			}
			// TODO: do this better
			// if img.MountState == pb.MountState_MountError {
			// 	log.Println("fixing", img.MountPoint)
			// 	img.MountState = pb.MountState_Mounted
			// 	img.ImageSize = 0
			// 	toRestore = append(toRestore, &img)
			// 	continue
			// }
			if img.MountState == pb.MountState_Mounted {
				toRestore = append(toRestore, &img)
			}
		}
		return nil
	})
	for _, img := range toRestore {
		if mounted, err := isErofsMount(img.MountPoint); err == nil && mounted {
			// log.Print("restoring: ", img.StorePath, " already mounted on ", img.MountPoint)
			continue
		}
		err := s.tryMount(context.Background(), &MountReq{
			StorePath:  img.StorePath,
			MountPoint: img.MountPoint,
			// the image has been written so we don't need upstream/narsize
		})
		if err == nil {
			log.Print("restoring: ", img.StorePath, " restored to ", img.MountPoint)
		} else {
			log.Print("restoring: ", img.StorePath, " error: ", err)
		}
	}
}
