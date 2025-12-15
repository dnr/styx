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

	// header value is base64(proto-encoded pb.Lengths)
	LengthsHeader = "x-styx-lengths"
)

type (
	ManifestReq struct {
		Upstream      string // url of nix binary cache or generic file
		StorePathHash string

		// TODO: this should really be a new request type
		BuildMode string `json:",omitempty"`

		// TODO: move this to pb and embed a GlobalParams?
		DigestAlgo string
		DigestBits int

		// used for tarball cache lookups only
		ETag string

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

	// Server will add header named LengthsHeader with lengths of req data for each individual
	// request. Note that if not expanding, the caller will already know that information,
	// since it knows the size of each requested chunk. If expanding, though, it doesn't know
	// the expanded size. If there's only one request, similarly, the caller can determine the
	// expanded size since it's the full reconstructed body (minus stats). But for more than
	// one request, there's no additional framing, so the length header will be needed to
	// separate them.

	// ChunkDiffStats must contain only integers! (For now, since we sometimes scan backwards
	// to find the start of the stats. We can relax this requirement if we write a reverse json
	// parser.)
	ChunkDiffStats struct {
		Reqs       int   `json:"reqs"`
		Expands    int   `json:"exps"`
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
	if (r.BuildMode == ModeGenericTarball) != (r.ETag != "") {
		panic("ETag must only be used with ModeGenericTarball")
	} else if (r.BuildMode == ModeNar) != (r.StorePathHash != "") {
		panic("StorePathHash must only be used with ModeNar")
	}

	h := sha256.New()
	h.Write([]byte("styx-manifest-cache-v1\n"))
	h.Write([]byte(fmt.Sprintf("u=%s\n", r.Upstream)))
	switch r.BuildMode {
	case ModeNar:
		h.Write([]byte(fmt.Sprintf("h=%s\n", r.StorePathHash)))
	case ModeGenericTarball:
		h.Write([]byte(fmt.Sprintf("m=%s\n", ModeGenericTarball)))
		h.Write([]byte(fmt.Sprintf("e=%s\n", r.ETag)))
	default:
		panic("unknown BuildMode")
	}
	// use fixed 16 for compatibility, chunk size is now variable
	h.Write([]byte(fmt.Sprintf("p=16:%s:%d\n", r.DigestAlgo, r.DigestBits)))
	// note: SmallFileCutoff is not part of key, client may get different one from requested
	return "v1-" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))[:36]
}
