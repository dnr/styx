package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/anatol/devmapper.go"
	"github.com/dnr/styx/common"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
	"github.com/google/uuid"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

// on-demand mount management

func (s *Server) tryMount(ctx context.Context, req *MountReq) error {
	_, sphStr, _ := ParseSph(req.StorePath)

	var imgOff, imgLen uint32

	err := s.db.View(func(tx *bbolt.Tx) error {
		var img pb.DbImage
		if buf := tx.Bucket(imageBucket).Get([]byte(sphStr)); buf == nil {
			return nil
		} else if err := proto.Unmarshal(buf, &img); err != nil {
			return err
		}
		imgOff = common.TruncU32(img.ImageBlockStart)
		imgLen = common.TruncU32(img.ImageBlockLength)
		return nil
	})
	if err != nil {
		return err
	}

	var imagePrefix []byte
	if imgOff > 0 && imgLen > 0 {
		// we have it already, read first block out of the image slab
		imagePrefix := make([]byte, 4096)
		_, err = s.imageSlabF.ReadAt(imagePrefix, int64(imgOff)<<s.blockShift)
		if err != nil {
			return err
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
		imgLen = uint32(s.blockShift.Blocks(imgBytes))
		// allocate and write to image slab
		imgOff, err = s.allocateImageSpace(imgLen)
		if err != nil {
			return err
		}
		_, err = s.imageSlabF.WriteAt(image, int64(imgOff)<<s.blockShift)
		if err != nil {
			return err
		}
		err = s.imageTx(sphStr, func(img *pb.DbImage) error {
			img.ImageBlockStart = int64(imgOff)
			img.ImageBlockLength = int64(imgLen)
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
	for i, slabId := range slabsUsed {
		if slabId >= 0 {
			devs[i] = "device=" + s.slabPath("clone", uint16(slabId))
		} else {
			log.Printf("couldn't parse slab tag at index %i in image %s", i, sphStr)
			devs[i] = "device=/dev/null"
		}
	}
	opts := strings.Join(devs, ",")

	// set up dm linear for image
	dmName := "styx-image-" + sphStr
	var devNum uint64
	var dmPath string
	if di, err := devmapper.InfoByName(dmName); err == nil {
		devNum = di.DevNo
		dmPath = fmt.Sprintf("/dev/dm-%d", unix.Minor(devNum))
	} else {
		devNum, err = devmapper.Create(dmName, uuid.NewString())
		if err != nil {
			return fmt.Errorf("dm create %q: %w", dmName, err)
		}
		dmPath = fmt.Sprintf("/dev/dm-%d", unix.Minor(devNum))
		defer s.markForUdev(dmPath)()
		tab := &devmapper.LinearTable{
			Start:         0,
			Length:        uint64(imgLen) << s.blockShift,
			BackendDevice: s.imageSlabLo.Path(),
			BackendOffset: uint64(imgOff) << s.blockShift,
		}
		if err = devmapper.Load(dmName, devmapper.ReadOnlyFlag, tab); err != nil {
			return fmt.Errorf("dm load %q: %w", dmName, err)
		} else if err = devmapper.Resume(dmName); err != nil {
			return fmt.Errorf("dm resume %q: %w", dmName, err)
		}
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
