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

	"github.com/klauspost/compress/zstd"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

func (s *server) getManifestAndBuildImage(ctx context.Context, req *MountReq) ([]byte, error) {
	cookie, _, _ := strings.Cut(req.StorePath, "-")

	// convert to binary
	var sph Sph
	if n, err := nixbase32.Decode(sph[:], []byte(cookie)); err != nil || n != len(sph) {
		return nil, fmt.Errorf("cookie is not a valid nix store path hash")
	}
	// use a separate "sph" for the manifest itself (a single entry). only used if manifest is chunked.
	manifestSph := makeManifestSph(sph)

	ctx = context.WithValue(ctx, "sph", sph)

	// get manifest
	envelopeBytes, err := s.getManifestFromManifester(ctx, req.Upstream, cookie)
	if err != nil {
		return nil, err
	}

	// verify signature and params
	entry, smParams, err := common.VerifyMessageAsEntry(s.cfg.StyxPubKeys, common.ManifestContext, envelopeBytes)
	if err != nil {
		return nil, err
	}
	if smParams != nil {
		gParams := s.cfg.Params.Params
		match := smParams.ChunkShift == gParams.ChunkShift &&
			smParams.DigestBits == gParams.DigestBits &&
			smParams.DigestAlgo == gParams.DigestAlgo
		if !match {
			return nil, fmt.Errorf("chunked manifest global params mismatch")
		}
	}

	// check entry path to get storepath
	storePath := strings.TrimPrefix(entry.Path, common.ManifestContext+"/")
	if storePath != req.StorePath {
		return nil, fmt.Errorf("envelope storepath != requested storepath: %q != %q", storePath, req.StorePath)
	}
	spHash, spName, _ := strings.Cut(storePath, "-")
	if spHash != cookie || len(spName) == 0 {
		return nil, fmt.Errorf("invalid or mismatched name in manifest %q", storePath)
	}

	// record signed manifest message in db
	if err = s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(manifestBucket).Put([]byte(cookie), envelopeBytes)
	}); err != nil {
		return nil, err
	}
	// update catalog with this envelope (and manifest entry). should match code in initCatalog.
	s.catalog.add(storePath)

	// get payload or load from chunks
	data := entry.InlineData
	if len(data) == 0 {
		s.catalog.add(manifestSph.String() + "-" + isManifestPrefix + spName)

		log.Printf("loading chunked manifest for %s", storePath)

		// allocate space for manifest chunks in slab
		blocks := make([]uint16, 0, len(entry.Digests)/s.digestBytes)
		blocks = common.AppendBlocksList(blocks, entry.Size, s.chunkShift, s.blockShift)

		ctxForManifestChunks := context.WithValue(ctx, "sph", manifestSph)
		locs, err := s.AllocateBatch(ctxForManifestChunks, blocks, entry.Digests, true)
		if err != nil {
			return nil, err
		}

		// read them out
		data, err = s.readChunks(ctx, nil, entry.Size, locs, entry.Digests, []Sph{manifestSph}, true)
		if err != nil {
			return nil, err
		}
	}

	// unmarshal into manifest
	var m pb.Manifest
	err = proto.Unmarshal(data, &m)
	if err != nil {
		return nil, fmt.Errorf("manifest unmarshal error: %w", err)
	}

	// make sure this matches the name in the envelope and original request
	if niStorePath := path.Base(m.Meta.GetNarinfo().GetStorePath()); niStorePath != storePath {
		return nil, fmt.Errorf("narinfo storepath != envelope storepath: %q != %q", niStorePath, storePath)
	}

	// transform manifest into image (allocate chunks)
	var image bytes.Buffer
	err = s.builder.BuildFromManifestWithSlab(ctx, &m, &image, s)
	if err != nil {
		return nil, fmt.Errorf("build image error: %w", err)
	}

	log.Printf("new image %s: %d envelope, %d manifest, %d erofs", storePath, len(envelopeBytes), entry.Size, image.Len())
	return image.Bytes(), nil
}

func (s *server) getManifestFromManifester(ctx context.Context, upstream, sph string) ([]byte, error) {
	// check cached first
	gParams := s.cfg.Params.Params
	mReq := manifester.ManifestReq{
		Upstream:      upstream,
		StorePathHash: sph,
		ChunkShift:    int(gParams.ChunkShift),
		DigestAlgo:    gParams.DigestAlgo,
		DigestBits:    int(gParams.DigestBits),
		// SmallFileCutoff: s.cfg.SmallFileCutoff,
	}
	s.stats.manifestCacheReqs.Add(1)
	if b, err := s.mcread.Get(ctx, mReq.CacheKey(), nil); err == nil {
		log.Printf("got manifest for %s from cache", sph)
		s.stats.manifestCacheHits.Add(1)
		return b, nil
	} else if common.IsContextError(err) {
		return nil, err
	}
	// not found cached, request it
	u := strings.TrimSuffix(s.cfg.Params.ManifesterUrl, "/") + manifester.ManifestPath
	s.stats.manifestReqs.Add(1)
	b, err := getNewManifest(ctx, u, mReq)
	if err != nil {
		s.stats.manifestErrs.Add(1)
		return nil, err
	}
	return b, nil
}

func getNewManifest(ctx context.Context, url string, req manifester.ManifestReq) ([]byte, error) {
	log.Print("requesting manifest for", req.StorePathHash)
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	res, err := retryHttpRequest(ctx, http.MethodPost, url, "application/json", reqBytes)
	if err != nil {
		return nil, fmt.Errorf("manifester http error: %w", err)
	}
	defer res.Body.Close()
	if zr, err := zstd.NewReader(res.Body); err != nil {
		return nil, err
	} else if b, err := io.ReadAll(zr); err != nil {
		return nil, err
	} else {
		log.Print("got manifest for", req.StorePathHash)
		return b, nil
	}
}
