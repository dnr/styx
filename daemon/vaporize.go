package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path"
	"time"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.etcd.io/bbolt"
)

func (s *Server) handleVaporizeReq(ctx context.Context, r *VaporizeReq) (*Status, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}

	storePath := r.Name
	if storePath == "" {
		storePath = path.Base(r.Path)
	}

	if !reStorePath.MatchString(storePath) {
		return nil, mwErr(http.StatusBadRequest, "invalid store path or missing name")
	}

	sph, _, err := ParseSph(storePath)
	if err != nil {
		return nil, err
	}
	manifestSph := makeManifestSph(sph)

	// FIXME: actually do this without nar dump and manifest builder and erofs builder
	// so we can copyfilerange into the slab.

	// set up manifest builder
	mbcfg := manifester.ManifestBuilderConfig{}
	memcs := memChunkStore{m: make(map[cdig.CDig][]byte), blkshift: s.blockShift}
	mb, err := manifester.NewManifestBuilder(mbcfg, &memcs)
	if err != nil {
		return nil, err
	}

	// dump path to nar
	dump := exec.CommandContext(ctx, common.NixBin+"-store", "--dump", r.Path)
	narOut, err := dump.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = dump.Start(); err != nil {
		return nil, err
	}
	defer func() {
		if dump != nil {
			dump.Process.Kill()
			dump.Wait()
		}
	}()

	// build manifest from nar
	args := manifester.BuildArgs{SmallFileCutoff: manifester.DefaultSmallFileCutoff}
	manifest, err := mb.BuildFromNar(ctx, &args, narOut)
	if err != nil {
		return nil, fmt.Errorf("manifest generation error: %w", err)
	}
	if err = dump.Wait(); err != nil {
		return nil, fmt.Errorf("nar dump error: %w", err)
	}
	dump = nil

	// set up manifest meta
	manifest.Meta = &pb.ManifestMeta{
		NarinfoUrl: "<vaporize>",
		Narinfo: &pb.NarInfo{
			StorePath: storepath.StoreDir + "/" + storePath,
			Url:       "vaporize://" + r.Path,
		},
		Generator:     "styx-" + common.Version + " (vaporize)",
		GeneratedTime: time.Now().Unix(),
	}

	// get entry for manifest
	args = manifester.BuildArgs{SmallFileCutoff: manifester.SmallManifestCutoff}
	path := common.ManifestContext + "/" + storePath
	entry, err := mb.ManifestAsEntry(ctx, &args, path, manifest)
	if err != nil {
		return nil, err
	}

	// FIXME: write entry to manifest bucket?

	// FIXME: write to forwards/backwards catalog

	// allocate data chunks in slab. this just calls erofs.Builder. this is doing too much
	// work, we really only need the s.AllocateBatch calls. can optimize it later.
	ctxForChunks := context.WithValue(ctx, "sph", sph)
	err = s.builder.BuildFromManifestWithSlab(ctxForChunks, manifest, io.Discard, s)
	if err != nil {
		return nil, fmt.Errorf("build image error: %w", err)
	}

	// allocate space for manifest chunks in slab
	if len(entry.InlineData) == 0 {
		digests := cdig.FromSliceAlias(entry.Digests)
		blocks := make([]uint16, 0, len(digests))
		blocks = common.AppendBlocksList(blocks, entry.Size, s.blockShift)
		ctxForManifestChunks := context.WithValue(ctx, "sph", manifestSph)
		_, err := s.AllocateBatch(ctxForManifestChunks, blocks, digests, true)
		if err != nil {
			return nil, err
		}
	}

	type writeRec struct {
		dig  cdig.CDig
		loc  erofs.SlabLoc
		data []byte
	}
	toWrite := make([]writeRec, 0, len(memcs.m))
	_ = s.db.Update(func(tx *bbolt.Tx) error {
		for dig, data := range memcs.m {
			if loc, present := s.digestPresent(tx, dig); !present {
				toWrite = append(toWrite, writeRec{dig: dig, loc: loc, data: data})
			}
		}
		return nil
	})

	// write all new chunks to slab
	for _, rec := range toWrite {
		err = s.gotNewChunk(rec.loc, rec.dig, rec.data)
		if err != nil {
			return nil, err
		}
	}

	return nil, nil
}

type memChunkStore struct {
	m        map[cdig.CDig][]byte
	blkshift common.BlkShift
}

func (m *memChunkStore) PutIfNotExists(ctx context.Context, path string, key string, data []byte) ([]byte, error) {
	if path == manifester.ChunkReadPath {
		dig, err := cdig.FromBase64(key)
		if err != nil {
			return nil, err
		}
		// data is from a chunk pool, we shouldn't hold on to it. make a copy. but leave extra
		// room so we don't have to copy again when we write.
		d := make([]byte, len(data), m.blkshift.Roundup(int64(len(data))))
		copy(d, data)
		m.m[dig] = d
	}
	return nil, nil
}

func (m *memChunkStore) Get(ctx context.Context, path string, key string, dst []byte) ([]byte, error) {
	panic("not implemented")
}
