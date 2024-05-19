package manifester

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const (
	ChunkDiffMaxDigests = 256
)

var (
	// protocol is (mostly) json over http
	ManifestPath  = "/manifest"
	ChunkDiffPath = "/chunkdiff"

	// chunk read protocol
	ChunkReadPath     = "/chunk/"    // digest as final path component
	ManifestCachePath = "/manifest/" // cache key as final path component

	ExpandGz = "gz"
	ExpandXz = "xz"
)

type (
	ManifestReq struct {
		Upstream      string
		StorePathHash string

		// TODO: move this to pb and embed a GlobalParams?
		ChunkShift int
		DigestAlgo string
		DigestBits int

		// sharded manifesting (not in cache key, only shard 0 writes to cache)
		ShardTotal int
		ShardIndex int
	}
	// response is SignedManifest

	// TODO: move this to pb so we don't have to use base64?
	ChunkDiffReq struct {
		Bases []byte
		Reqs  []byte

		// If set: Bases and Reqs each comprise one single file in the given compression
		// format. Pass each one through this decompressor before diffing.
		ExpandBeforeDiff string `json:",omitempty"`
	}
	// Response is compressed concatenation of reqs, using bases as compression base,
	// with ChunkDiffStats (json) appended after that (also compressed).
	// Bases and Reqs do not need to be the same length.
	// (Caller must know the lengths of reqs ahead of time to be able to split the result.)
	// Max number of digests in each is 256. With 64KiB chunks, that makes 16MiB total data.
	ChunkDiffStats struct {
		BaseChunks int   `json:"baseC"`
		BaseBytes  int   `json:"baseB`
		ReqChunks  int   `json:"reqC"`
		ReqBytes   int   `json:"reqB"`
		DiffBytes  int   `json:"diffB"`
		DlTotalMs  int64 `json:"dlMs"`
		ZstdMs     int64 `json:"zstdMs"`
	}
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
