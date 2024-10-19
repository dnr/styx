package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/DataDog/zstd"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

func (s *Server) getManifestAndBuildImage(ctx context.Context, req *MountReq) (*pb.Manifest, []byte, error) {
	cookie, _, _ := strings.Cut(req.StorePath, "-")

	// convert to binary
	var sph Sph
	if n, err := nixbase32.Decode(sph[:], []byte(cookie)); err != nil || n != len(sph) {
		return nil, nil, fmt.Errorf("cookie is not a valid nix store path hash")
	}
	// use a separate "sph" for the manifest itself (a single entry). only used if manifest is chunked.
	manifestSph := makeManifestSph(sph)
	manifestSphPrefix := SphPrefixFromBytes(manifestSph[:sphPrefixBytes])

	// get manifest
	envelopeBytes, err := s.getManifestFromManifester(ctx, req.Upstream, cookie, req.NarSize)
	if err != nil {
		return nil, nil, err
	}

	// verify signature and params
	entry, smParams, err := common.VerifyMessageAsEntry(s.p().keys, common.ManifestContext, envelopeBytes)
	if err != nil {
		return nil, nil, err
	}
	if smParams != nil {
		match := smParams.ChunkShift == int32(common.ChunkShift) &&
			smParams.DigestBits == cdig.Bits &&
			smParams.DigestAlgo == common.DigestAlgo
		if !match {
			return nil, nil, fmt.Errorf("chunked manifest global params mismatch")
		}
	}

	// check entry path to get storepath
	storePath := strings.TrimPrefix(entry.Path, common.ManifestContext+"/")
	if storePath != req.StorePath {
		return nil, nil, fmt.Errorf("envelope storepath != requested storepath: %q != %q", storePath, req.StorePath)
	}
	spHash, spName, _ := strings.Cut(storePath, "-")
	if spHash != cookie || len(spName) == 0 {
		return nil, nil, fmt.Errorf("invalid or mismatched name in manifest %q", storePath)
	}

	// record signed manifest message in db and add names to catalog
	if err = s.db.Update(func(tx *bbolt.Tx) error {
		mb := tx.Bucket(manifestBucket)
		if err := mb.Put([]byte(cookie), envelopeBytes); err != nil {
			return err
		}

		cfb := tx.Bucket(catalogFBucket)
		crb := tx.Bucket(catalogRBucket)
		key := bytes.Join([][]byte{[]byte(spName), []byte{0}, sph[:]}, nil)
		val := []byte{} // TODO: put sysid in here
		if err := cfb.Put(key, val); err != nil {
			return err
		} else if err = crb.Put(sph[:], []byte(spName)); err != nil {
			return err
		}
		if len(entry.InlineData) == 0 {
			mkey := bytes.Join([][]byte{[]byte(isManifestPrefix), []byte(spName), []byte{0}, manifestSph[:]}, nil)
			if err := cfb.Put(mkey, nil); err != nil {
				return err
			} else if err = crb.Put(manifestSph[:], []byte(isManifestPrefix+spName)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}

	// get payload or load from chunks
	data := entry.InlineData
	if len(data) == 0 {
		log.Printf("loading chunked manifest for %s", storePath)

		// allocate space for manifest chunks in slab
		digests := cdig.FromSliceAlias(entry.Digests)
		blocks := make([]uint16, 0, len(digests))
		blocks = common.AppendBlocksList(blocks, entry.Size, s.blockShift)

		ctxForManifestChunks := withAllocateCtx(ctx, manifestSph, true)
		locs, err := s.AllocateBatch(ctxForManifestChunks, blocks, digests)
		if err != nil {
			return nil, nil, err
		}

		// read them out
		data, err = s.readChunks(ctx, nil, entry.Size, locs, digests, []SphPrefix{manifestSphPrefix}, true)
		if err != nil {
			return nil, nil, err
		}
	}

	// unmarshal into manifest
	var m pb.Manifest
	err = proto.Unmarshal(data, &m)
	if err != nil {
		return nil, nil, fmt.Errorf("manifest unmarshal error: %w", err)
	}

	// make sure this matches the name in the envelope and original request
	if niStorePath := path.Base(m.Meta.GetNarinfo().GetStorePath()); niStorePath != storePath {
		return nil, nil, fmt.Errorf("narinfo storepath != envelope storepath: %q != %q", niStorePath, storePath)
	}

	// transform manifest into image (allocate chunks)
	var image bytes.Buffer
	ctxForChunks := withAllocateCtx(ctx, sph, false)
	err = s.builder.BuildFromManifestWithSlab(ctxForChunks, &m, &image, s)
	if err != nil {
		return nil, nil, fmt.Errorf("build image error: %w", err)
	}

	log.Printf("new image %s: %d envelope, %d manifest, %d erofs", storePath, len(envelopeBytes), entry.Size, image.Len())
	return &m, image.Bytes(), nil
}

func (s *Server) getManifestFromManifester(ctx context.Context, upstream, sph string, narSize int64) ([]byte, error) {
	// check cached first
	mReq := manifester.ManifestReq{
		Upstream:      upstream,
		StorePathHash: sph,
		ChunkShift:    int(common.ChunkShift),
		DigestAlgo:    common.DigestAlgo,
		DigestBits:    int(cdig.Bits),
		// SmallFileCutoff: s.cfg.SmallFileCutoff,
	}

	// check cache
	s.stats.manifestCacheReqs.Add(1)
	if b, err := s.p().mcread.Get(ctx, mReq.CacheKey(), nil); err == nil {
		log.Printf("got manifest for %s from cache", sph)
		s.stats.manifestCacheHits.Add(1)
		return b, nil
	} else if common.IsContextError(err) {
		return nil, err
	}

	// not found cached, request it
	u := strings.TrimSuffix(s.p().params.ManifesterUrl, "/") + manifester.ManifestPath
	s.stats.manifestReqs.Add(1)
	b, err := s.getNewManifest(ctx, u, mReq, narSize)
	if err != nil {
		s.stats.manifestErrs.Add(1)
		return nil, err
	}
	return b, nil
}

func (s *Server) getNewManifest(ctx context.Context, url string, req manifester.ManifestReq, narSize int64) ([]byte, error) {
	start := time.Now()

	shardBy := s.p().params.ShardManifestBytes
	if shardBy == 0 {
		// aim for max 10 seconds
		shardBy = 20 << 20
	}

	shards := max(min(int((narSize+shardBy-1)/shardBy), 40), 1)
	log.Printf("requesting manifest for %s with %d shards", req.StorePathHash, shards)
	egCtx := errgroup.WithContext(ctx)

	var shard0 []byte
	for i := 0; i < shards; i++ {
		egCtx.Go(func() error {
			thisReq := req
			thisReq.ShardTotal = shards
			thisReq.ShardIndex = i
			reqBytes, err := json.Marshal(thisReq)
			if err != nil {
				return err
			}
			res, err := retryHttpRequest(egCtx, http.MethodPost, url, "application/json", reqBytes)
			if err != nil {
				return fmt.Errorf("manifester http error: %w", err)
			}
			defer res.Body.Close()
			if i == 0 {
				if b, err := io.ReadAll(zstd.NewReader(res.Body)); err != nil {
					return err
				} else {
					shard0 = b
				}
			} else {
				io.Copy(io.Discard, res.Body)
			}
			return nil
		})
	}

	err := egCtx.Wait()
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(start)
	log.Printf("got manifest for %s in %.2fs with %d shards", req.StorePathHash, elapsed.Seconds(), shards)
	return shard0, nil
}
