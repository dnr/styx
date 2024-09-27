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

	"github.com/nix-community/go-nix/pkg/nixbase32"
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
	cookie, _, _ := strings.Cut(r.StorePath, "-")
	var sph Sph
	if n, err := nixbase32.Decode(sph[:], []byte(cookie)); err != nil || n != len(sph) {
		return nil, mwErr(http.StatusBadRequest, "path is not a valid store path")
	}
	mp := filepath.Join(s.cfg.CachePath, "materialize", cookie)

	// TODO: handle bare files
	// TODO: we don't actually need to mount it, we can just get as far as allocating chunks in
	// the slab. don't even need erofs image. but we do need entries in catalog for diffing.

	// clean up
	defer s.handleUmountReq(ctx, &UmountReq{StorePath: cookie})

	// mount internally
	_, err := s.handleMountReq(ctx, &MountReq{
		Upstream:   r.Upstream,
		StorePath:  r.StorePath,
		MountPoint: mp,
		NarSize:    r.NarSize,
	})
	if err != nil {
		return nil, err
	}

	// prefetch all
	_, err = s.handlePrefetchReq(ctx, &PrefetchReq{
		Path:      "/",
		StorePath: cookie,
	})
	if err != nil {
		return nil, err
	}

	// copy to dest
	return nil, s.materialize(r.DestPath, sph, mp)
}

func (s *Server) materialize(dest string, sph Sph, mp string) error {
	var ents []*pb.Entry
	locs := make(map[cdig.CDig]erofs.SlabLoc)

	err := s.db.View(func(tx *bbolt.Tx) error {
		cb := tx.Bucket(chunkBucket)

		var err error
		ents, err = s.getDigestsFromImage(tx, sph, false)
		if err != nil {
			return fmt.Errorf("can't read manifest: %w", err)
		}

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
	for _, ent := range ents {
		p := filepath.Join(dest, ent.Path)

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
			if err := os.Symlink(string(ent.InlineData), p); err != nil {
				return err
			}
		default:
			return errors.New("unknown entry type in manifest")
		}
	}

	return nil
}
