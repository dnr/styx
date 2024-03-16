package manifester

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

var (
	// protocol is (mostly) json over http
	ManifestPath  = "/manifest"
	ChunkDiffPath = "/chunkdiff"

	// chunk read protocol
	ChunkReadPath     = "/chunk/"    // digest as final path component
	ManifestCachePath = "/manifest/" // cache key as final path component
)

type (
	ManifestReq struct {
		Upstream      string
		StorePathHash string

		// TODO: move this to pb and embed a GlobalParams?
		ChunkShift int
		DigestAlgo string
		DigestBits int

		SmallFileCutoff int
	}
	// response is SignedManifest

	ChunkDiffReq struct {
		From, To    string
		AcceptAlgos []string
	}
	// response is binary diff
)

func (r *ManifestReq) CacheKey() string {
	h := sha256.New()
	h.Write([]byte("styx-manifest-cache-v1\n"))
	h.Write([]byte(fmt.Sprintf("u=%s\n", r.Upstream)))
	h.Write([]byte(fmt.Sprintf("h=%s\n", r.StorePathHash)))
	h.Write([]byte(fmt.Sprintf("p=%d:%s:%d\n", r.ChunkShift, r.DigestAlgo, r.DigestBits)))
	// note: SmallFileCutoff is not part of key, client may get different one from requested
	return "v1-" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))[:36]
}
