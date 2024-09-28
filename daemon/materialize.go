package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
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
			m, err = s.getManifestLocal(tx, []byte(sphStr))
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
	buf := make([]byte, common.ChunkShift.Size())
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
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case pb.EntryType_REGULAR:
			mode := os.FileMode(0o644)
			if ent.Executable {
				mode = 0o755
			}
		tryAgain:
			dst, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if len(ent.InlineData) > 0 {
				if _, err = dst.Write(ent.InlineData); err != nil {
					return cmp.Or(err, dst.Close())
				}
			}
			digs := cdig.FromSliceAlias(ent.Digests)
			for i, dig := range digs {
				size := common.ChunkShift.FileChunkSize(ent.Size, i == len(digs)-1)
				if tryClone {
					loc := locs[dig]
					err := errCachefdNotFound
					if cfd := readFds[loc.SlabId].cacheFd; cfd > 0 {
						sizeUp := int(s.blockShift.Roundup(size))
						roff := int64(loc.Addr) << s.blockShift
						woff := int64(i) << common.ChunkShift
						var rsize int
						rsize, err = unix.CopyFileRange(cfd, &roff, int(dst.Fd()), &woff, sizeUp, 0)
						if err == nil && rsize != sizeUp {
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
					}
					if err != nil {
						switch err {
						case syscall.EINVAL, syscall.EOPNOTSUPP, syscall.EXDEV, io.ErrShortWrite, errCachefdNotFound:
							log.Println("error from CopyFileRange, falling back to plain copy:", err)
							tryClone = false
							dst.Close()
							goto tryAgain
						default:
							return err
						}
					}
				} else {
					b := buf[:size]
					if err = s.getKnownChunk(locs[dig], b); err != nil {
						return cmp.Or(err, dst.Close())
					} else if _, err := dst.Write(b); err != nil {
						return cmp.Or(err, dst.Close())
					}
				}
			}
			if tryClone {
				// we have to round up blocks when using CopyFileRange, so truncate the last one
				unix.Ftruncate(int(dst.Fd()), ent.Size)
			}
			if err = dst.Close(); err != nil {
				return err
			}
		case pb.EntryType_SYMLINK:
			if i == 0 {
				return errors.New("bare file can't be symlink")
			}
			if err := os.Symlink(string(ent.InlineData), p); err != nil {
				return err
			}
		default:
			return errors.New("unknown entry type in manifest")
		}
	}

	return nil
}
