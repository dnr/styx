package daemon

import (
	"context"
	"net/http"
	"strings"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.etcd.io/bbolt"
)

func (s *server) handlePrefetchReq(ctx context.Context, r *PrefetchReq) (*Status, error) {
	haveReq := make(map[cdig.CDig]struct{})
	var reqs []cdig.CDig
	var reqSizes []int64
	var cres catalogResult

	err := s.db.View(func(tx *bbolt.Tx) error {
		// TODO: to simplify things, this only supports images mounted at the standard
		// location. we can relax this if we keep a trie of active mounts.
		p := r.Path
		if !underDir(p, storepath.StoreDir) || p == storepath.StoreDir {
			return mwErr(http.StatusBadRequest, "path is not a valid store path")
		}
		p = p[len(storepath.StoreDir)+1:] // p starts with store path now
		storePath, _, _ := strings.Cut(p, "/")
		sphStr, name, _ := strings.Cut(storePath, "-")
		var sph Sph
		if n, err := nixbase32.Decode(sph[:], []byte(sphStr)); err != nil || n != len(sph) || len(name) == 0 {
			return mwErr(http.StatusBadRequest, "path is not a valid store path")
		}
		ents, err := s.getDigestsFromImage(ctx, tx, sph, false)
		if err != nil {
			return mwErr(http.StatusInternalServerError, "can't read manifest")
		}
		// strip off mountpoint. p should now have an initial /
		p = p[len(storePath):]
		if len(p) == 0 {
			p = "/"
		}
		for _, e := range ents {
			if len(e.Digests) > 0 && underDir(e.Path, p) {
				digests := cdig.FromSliceAlias(e.Digests)
				for digestIdx, d := range digests {
					// just do a simple de-dup here, don't check presence
					if _, ok := haveReq[d]; !ok {
						haveReq[d] = struct{}{}
						reqs = append(reqs, d)
						size := common.ChunkShift.FileChunkSize(e.Size, digestIdx == len(digests)-1)
						reqSizes = append(reqSizes, size)
					}
				}
			}
		}
		if len(reqs) == 0 {
			return nil
		}
		// look this up while we have the tx
		cres, err = s.catalogFindBaseFromHashAndName(tx, sph, storePath)
		if err != nil {
			cres = catalogResult{
				reqName:  name,
				baseName: noBaseName,
				reqHash:  sph,
			}
		}
		return nil
	})
	if err != nil || len(reqs) == 0 {
		return nil, err
	}
	return nil, s.requestPrefetch(ctx, reqs, reqSizes, cres)
}
