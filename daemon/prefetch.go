package daemon

import (
	"context"
	"net/http"
	"strings"

	"github.com/dnr/styx/common/cdig"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.etcd.io/bbolt"
)

func (s *Server) handlePrefetchReq(ctx context.Context, r *PrefetchReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}

	haveReq := make(map[cdig.CDig]struct{})
	var reqs []cdig.CDig

	err := s.db.View(func(tx *bbolt.Tx) error {
		var sphStr string
		p := r.Path
		if r.StorePath == "" {
			// TODO: to simplify things, this only supports images mounted at the standard
			// location. we can relax this if we keep a trie of active mounts.
			if !underDir(p, storepath.StoreDir) || p == storepath.StoreDir {
				return mwErr(http.StatusBadRequest, "path is not a valid store path")
			}
			p = p[len(storepath.StoreDir)+1:] // p starts with store path now
			storePath, _, _ := strings.Cut(p, "/")
			sphStr, _, _ = strings.Cut(storePath, "-")
			// strip off mountpoint. p should now have an initial /
			p = p[len(storePath):]
			if len(p) == 0 {
				p = "/"
			}
		} else {
			sphStr = r.StorePath
		}
		var sph Sph
		if n, err := nixbase32.Decode(sph[:], []byte(sphStr)); err != nil || n != len(sph) {
			return mwErr(http.StatusBadRequest, "path is not a valid store path")
		}
		ents, err := s.getDigestsFromImage(tx, sph, false)
		if err != nil {
			return mwErr(http.StatusInternalServerError, "can't read manifest")
		}
		for _, e := range ents {
			if len(e.Digests) > 0 && underDir(e.Path, p) {
				digests := cdig.FromSliceAlias(e.Digests)
				for _, d := range digests {
					// just do a simple de-dup here, don't check presence
					if _, ok := haveReq[d]; !ok {
						haveReq[d] = struct{}{}
						reqs = append(reqs, d)
					}
				}
			}
		}
		if len(reqs) == 0 {
			return nil
		}
		return nil
	})
	if err != nil || len(reqs) == 0 {
		return nil, err
	}
	return nil, s.requestPrefetch(ctx, reqs)
}
