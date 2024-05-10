package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/manifester"
)

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
