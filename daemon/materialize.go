package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dnr/styx/pb"
	"github.com/nix-community/go-nix/pkg/nixbase32"
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
	tx, err := s.db.Begin(false)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ents, err := s.getDigestsFromImage(tx, sph, false)
	if err != nil {
		return fmt.Errorf("can't read manifest: %w", err)
	}

	tryCloneRange := true
	copyBuf := make([]byte, 1<<20)

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
				src := filepath.Join(mp, ent.Path)
				if err = plainCopy(p, src, mode, copyBuf); err != nil {
					return err
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

func plainCopy(dstp, srcp string, mode os.FileMode, buf []byte) (retErr error) {
	dst, err := os.OpenFile(dstp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() { retErr = cmp.Or(retErr, dst.Close()) }()
	src, err := os.Open(srcp)
	if err != nil {
		return err
	}
	defer func() { retErr = cmp.Or(retErr, src.Close()) }()
	// we can't use copy_file_range or splice or anything here, but Go will try and
	// then fall back to a tiny buffer, unless we mask ReadFrom/WriteTo first.
	_, err = io.CopyBuffer(fileWithoutMethods{File: dst}, fileWithoutMethods{File: src}, buf)
	return err
}

// inspired by Go stdlib:
type fileWithoutMethods struct {
	maskMethods
	*os.File
}
type maskMethods struct{}

func (maskMethods) WriteTo(io.Writer) (int64, error)  { panic(nil) }
func (maskMethods) ReadFrom(io.Reader) (int64, error) { panic(nil) }
