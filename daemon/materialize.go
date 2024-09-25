package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/nix-community/go-nix/pkg/nixbase32"
	"go.etcd.io/bbolt"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
)

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

	tryCloneRange := true

	// TODO: parallelize some of this, at least the reading?
	buf := make([]byte, common.ChunkShift.Size())
	for _, ent := range ents {
		p := filepath.Join(dest, ent.Path)

		switch ent.Type {
		case pb.EntryType_DIRECTORY:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case pb.EntryType_REGULAR:
			// notes: btrfs max inline is 2048?!
			// notes: ext4 inline is: 60 bytes
			mode := os.FileMode(0o644)
			if ent.Executable {
				mode = 0o755
			}
			if len(ent.Digests) == 0 {
				// empty or inline file
				if err = os.WriteFile(p, ent.InlineData, mode); err != nil {
					return err
				}
			} else if tryCloneRange {
				panic("FIXME")
			} else {
				dst, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
				if err != nil {
					return err
				}
				digs := cdig.FromSliceAlias(ent.Digests)
				for i, dig := range digs {
					size := common.ChunkShift.FileChunkSize(ent.Size, i == len(digs)-1)
					b := buf[:size]
					if err = s.getKnownChunk(locs[dig], b); err != nil {
						return err
					} else if _, err := dst.Write(b); err != nil {
						return err
					}
				}
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
