package manifester

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const (
	ChunkDiffMaxDigests = 256
	ChunkDiffMaxBytes   = 16 << 20
)

var (
	// protocol is (mostly) json over http
	ManifestPath  = "/manifest"
	ChunkDiffPath = "/chunkdiff"

	// chunk read protocol
	ChunkReadPath     = "/chunk/"    // digest as final path component
	ManifestCachePath = "/manifest/" // cache key as final path component

	BuildRootPath = "/buildroot/" // written by manifester, read only by gc

	ExpandGz = "gz"
	ExpandXz = "xz"
)

type (
	ManifestReq struct {
		Upstream      string
		StorePathHash string

		// TODO: move this to pb and embed a GlobalParams?
		DigestAlgo string
		DigestBits int

		// sharded manifesting (not in cache key, only shard 0 writes to cache)
		ShardTotal int `json:",omitempty"`
		ShardIndex int `json:",omitempty"`
	}
	// response is SignedManifest

	// deprecated: use pb.ManifesterChunkDiffReq
	DeprecatedChunkDiffReq struct {
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
	// Max number of digests in each is 256, and max size of base data or req data is 16 MiB.
	// (256 * 64 KiB chunks = 16 MiB, larger chunks have lower limit on digests.)
	// Note if running on lambda: after the first 6 MB of streamed data, bandwidth is limited.
	// So aim for responses to be < 6 MB.
	//
	// ChunkDiffStats must contain only integers! (For now, since we sometimes scan backwards
	// to find the start of the stats. We can relax this requirement if we write a reverse json
	// parser.)
	ChunkDiffStats struct {
		BaseChunks int   `json:"baseC"`
		BaseBytes  int   `json:"baseB"`
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
	// use fixed 16 for compatibility, chunk size is now variable
	h.Write([]byte(fmt.Sprintf("p=16:%s:%d\n", r.DigestAlgo, r.DigestBits)))
	// note: SmallFileCutoff is not part of key, client may get different one from requested
	return "v1-" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))[:36]
}
