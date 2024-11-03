package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
)

var errCachefdNotFound = errors.New("cache fd not found for slab")

func (s *Server) handleMaterializeReq(ctx context.Context, r *MaterializeReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}
	if !reStorePath.MatchString(r.StorePath) {
		return nil, mwErr(http.StatusBadRequest, "invalid store path or missing name")
	} else if r.Upstream == "" {
		return nil, mwErr(http.StatusBadRequest, "invalid upstream")
	} else if !strings.HasPrefix(r.DestPath, "/") {
		return nil, mwErr(http.StatusBadRequest, "dest must be absolute path")
	}

	_, sphStr, err := ParseSph(r.StorePath)
	if err != nil {
		return nil, err
	}

	common.NormalizeUpstream(&r.Upstream)

	shouldHaveManifest := false
	_ = s.imageTx(sphStr, func(img *pb.DbImage) error {
		switch img.MountState {
		case pb.MountState_Unknown:
			// we have no record of this, set it up
			img.StorePath = r.StorePath
			img.Upstream = r.Upstream
			img.MountState = pb.MountState_Requested
			return nil

		case pb.MountState_Mounted, pb.MountState_Unmounted, pb.MountState_Materialized:
			// we should already have a manifest locally. leave img alone.
			shouldHaveManifest = true
			return errors.New("rollback")

		default:
			// other states are errors/races, leave img alone.
			return errors.New("rollback")
		}
	})

	var m *pb.Manifest
	if shouldHaveManifest {
		// read locally
		err = s.db.View(func(tx *bbolt.Tx) error {
			m, _, err = s.getManifestLocal(tx, sphStr)
			return err
		})
		if err != nil {
			// fall back to remote manifest
			log.Print("error getting manifest locally, trying remote")
			shouldHaveManifest = false
		}
	}
	if !shouldHaveManifest {
		// get manifest and allocate
		m, _, err = s.getManifestAndBuildImage(ctx, &MountReq{
			Upstream:  r.Upstream,
			StorePath: r.StorePath,
			NarSize:   r.NarSize,
		})
	}
	if err != nil {
		return nil, err
	}

	// prefetch all
	haveReq := make(map[cdig.CDig]struct{})
	var reqs []cdig.CDig
	for _, e := range m.Entries {
		for _, d := range cdig.FromSliceAlias(e.Digests) {
			if _, ok := haveReq[d]; !ok {
				haveReq[d] = struct{}{}
				reqs = append(reqs, d)
			}
		}
	}
	if len(reqs) > 0 {
		if err = s.requestPrefetch(ctx, reqs); err != nil {
			return nil, err
		}
	}

	// copy to dest
	err = s.materialize(r.DestPath, m)
	if err != nil {
		return nil, err
	}

	// record as materialized/error, unless in some other state
	_ = s.imageTx(sphStr, func(img *pb.DbImage) error {
		if img.MountState != pb.MountState_Requested {
			return errors.New("rollback")
		}
		img.MountState = pb.MountState_Materialized
		return nil
	})

	return nil, nil
}

func (s *Server) materialize(dest string, m *pb.Manifest) error {
	ents := m.Entries
	locs := make(map[cdig.CDig]erofs.SlabLoc)
	err := s.db.View(func(tx *bbolt.Tx) error {
		cb := tx.Bucket(chunkBucket)
		for it := newDigestIterator(ents); it.ent() != nil; it.next(1) {
			dig := it.digest()
			if _, ok := locs[dig]; ok {
				continue
			}
			loc := cb.Get(dig[:])
			if loc == nil {
				return fmt.Errorf("missing reference for chunk %s", dig)
			}
			locs[dig] = loadLoc(loc)
		}
		return nil
	})
	if err != nil {
		return err
	}

	tryClone := true
	s.stateLock.Lock()
	readFds := maps.Clone(s.readfdBySlab)
	s.stateLock.Unlock()

	// TODO: parallelize some of this?
	wasBare := false
	for i, ent := range ents {
		p := filepath.Join(dest, ent.Path)

		if wasBare {
			return errors.New("bare file must be only entry")
		} else if i == 0 {
			p = dest
			wasBare = ent.Type == pb.EntryType_REGULAR
		}

		switch ent.Type {
		case pb.EntryType_DIRECTORY:
			if err = os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case pb.EntryType_REGULAR:
			if err = s.materializeFile(p, ent, locs, readFds, &tryClone); err != nil {
				return err
			}
		case pb.EntryType_SYMLINK:
			if i == 0 {
				return errors.New("bare file can't be symlink")
			}
			if err = os.Symlink(string(ent.InlineData), p); err != nil {
				return err
			}
		default:
			return errors.New("unknown entry type in manifest")
		}
	}

	return nil
}

func (s *Server) materializeFile(
	path string,
	ent *pb.Entry,
	locs map[cdig.CDig]erofs.SlabLoc,
	readFds map[uint16]slabFds,
	tryClone *bool,
) (retErr error) {
	var dst *os.File
	defer func() {
		if dst != nil {
			retErr = cmp.Or(retErr, dst.Close())
		}
	}()
tryAgain:
	var err error
	dst, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fs.FileMode(ent.FileMode()))
	if err != nil {
		return err
	}

	if len(ent.InlineData) > 0 {
		_, err = dst.Write(ent.InlineData)
		return err
	}

	var buf []byte
	digs := cdig.FromSliceAlias(ent.Digests)
	roundedUp := false
	for i, dig := range digs {
		loc := locs[dig]
		size := common.ChunkShift.FileChunkSize(ent.Size, i == len(digs)-1)
		if *tryClone {
			if cfd := readFds[loc.SlabId].cacheFd; cfd > 0 {
				sizeUp := int(s.blockShift.Roundup(size))
				roundedUp = sizeUp != int(size)
				roff := int64(loc.Addr) << s.blockShift
				woff := int64(i) << common.ChunkShift
				var rsize int
				rsize, err = unix.CopyFileRange(cfd, &roff, int(dst.Fd()), &woff, sizeUp, 0)
				if err == nil && rsize != sizeUp {
					// we rounded size up to our erofs block size (4k for now), but it's possible
					// the target fs is using larger blocks. in that case this may fail.
					err = io.ErrShortWrite
				}
				// err = unix.IoctlFileCloneRange(
				// 	int(dst.Fd()),
				// 	&unix.FileCloneRange{
				// 		Src_fd:      int64(cfd),
				// 		Src_offset:  uint64(loc.Addr) << s.blockShift,
				// 		Src_length:  uint64(sizeUp),
				// 		Dest_offset: uint64(i) << common.ChunkShift,
				// 	})
			} else {
				err = errCachefdNotFound
			}
			switch err {
			case nil:
				// nothing
			case syscall.EINVAL, syscall.EOPNOTSUPP, syscall.EXDEV, io.ErrShortWrite, errCachefdNotFound:
				log.Println("error from CopyFileRange, falling back to plain copy:", err)
				*tryClone = false
				dst.Close()
				goto tryAgain
			default:
				return err
			}
		} else {
			if buf == nil {
				buf = s.chunkPool.Get(int(common.ChunkShift.Size()))
				defer s.chunkPool.Put(buf)
			}
			b := buf[:size]
			if err = s.getKnownChunk(loc, b); err != nil {
				return err
			} else if _, err := dst.Write(b); err != nil {
				return err
			}
		}
	}
	if roundedUp {
		// we have to round up blocks when using CopyFileRange, so truncate the last one
		return unix.Ftruncate(int(dst.Fd()), ent.Size)
	}
	return nil
}
