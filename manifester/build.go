package manifester

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sync/atomic"
	"time"

	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/pb"
)

type (
	BuildArgs struct {
		SmallFileCutoff int

		ShardTotal int
		ShardIndex int

		chunkIndex int // internal use
	}

	ManifestBuilder struct {
		cs        ChunkStoreWrite
		chunksem  *semaphore.Weighted
		params    *pb.GlobalParams
		chunkPool *common.ChunkPool
		pubKeys   []signature.PublicKey
		signKeys  []signature.SecretKey

		stats atomicStats
	}

	atomicStats struct {
		Manifests       atomic.Int64
		Shards          atomic.Int64
		TotalChunks     atomic.Int64
		TotalUncmpBytes atomic.Int64
		NewChunks       atomic.Int64
		NewUncmpBytes   atomic.Int64
		NewCmpBytes     atomic.Int64
	}

	Stats struct {
		Manifests       int64
		Shards          int64
		TotalChunks     int64
		TotalUncmpBytes int64
		NewChunks       int64
		NewUncmpBytes   int64
		NewCmpBytes     int64
	}

	ManifestBuilderConfig struct {
		ConcurrentChunkOps int

		// Verify loaded narinfo against these keys. Nil means don't verify.
		PublicKeys []signature.PublicKey
		// Sign manifests with these keys.
		SigningKeys []signature.SecretKey
	}

	ManifestBuildRes struct {
		CacheKey string // path relative to ManifestCachePath
		Bytes    []byte
	}
)

var (
	ErrReq      = errors.New("request err")
	ErrNotFound = errors.New("not found")
	ErrInternal = errors.New("internal err")
)

func NewManifestBuilder(cfg ManifestBuilderConfig, cs ChunkStoreWrite) (*ManifestBuilder, error) {
	return &ManifestBuilder{
		cs:       cs,
		chunksem: semaphore.NewWeighted(int64(cmp.Or(cfg.ConcurrentChunkOps, 200))),
		params: &pb.GlobalParams{
			ChunkShift: int32(common.ChunkShift),
			DigestAlgo: common.DigestAlgo,
			DigestBits: int32(cdig.Bits),
		},
		chunkPool: common.NewChunkPool(common.ChunkShift),
		pubKeys:   cfg.PublicKeys,
		signKeys:  cfg.SigningKeys,
	}, nil
}

// If errors occur during building, stats might not be exactly right.
func (b *ManifestBuilder) Stats() Stats {
	return Stats{
		Manifests:       b.stats.Manifests.Load(),
		Shards:          b.stats.Shards.Load(),
		TotalChunks:     b.stats.TotalChunks.Load(),
		TotalUncmpBytes: b.stats.TotalUncmpBytes.Load(),
		NewChunks:       b.stats.NewChunks.Load(),
		NewUncmpBytes:   b.stats.NewUncmpBytes.Load(),
		NewCmpBytes:     b.stats.NewCmpBytes.Load(),
	}
}

func (b *ManifestBuilder) ClearStats() {
	b.stats.Manifests.Store(0)
	b.stats.Shards.Store(0)
	b.stats.TotalChunks.Store(0)
	b.stats.TotalUncmpBytes.Store(0)
	b.stats.NewChunks.Store(0)
	b.stats.NewUncmpBytes.Store(0)
	b.stats.NewCmpBytes.Store(0)
}

func (b *ManifestBuilder) Build(
	ctx context.Context,
	upstream, storePathHash string,
	shardTotal, shardIndex int,
	useLocalStoreDump string,
) (*ManifestBuildRes, error) {
	// get narinfo

	upstreamUrl, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	narinfoUrl := upstreamUrl.JoinPath(storePathHash + ".narinfo").String()
	res, err := http.Get(narinfoUrl)
	if err != nil {
		return nil, fmt.Errorf("%w: upstream http for %s: %w", ErrReq, narinfoUrl, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: upstream http for %s", ErrNotFound, narinfoUrl)
		}
		return nil, fmt.Errorf("%w: upstream http for %s: %s", ErrReq, narinfoUrl, res.Status)
	}

	var rawNarinfo bytes.Buffer
	ni, err := narinfo.Parse(io.TeeReader(res.Body, &rawNarinfo))
	if err != nil {
		return nil, fmt.Errorf("%w: narinfo parse for %s: %w", ErrReq, narinfoUrl, err)
	}

	// verify signature

	if !signature.VerifyFirst(ni.Fingerprint(), ni.Signatures, b.pubKeys) {
		return nil, fmt.Errorf("%w: signature validation failed for %s; narinfo %#v", ErrReq, narinfoUrl, ni)
	}

	log.Println("manifest", storePathHash, "got narinfo", ni.StorePath[44:], ni.FileSize, ni.NarSize)

	// download nar
	var narOut io.Reader
	var dump, decompress *exec.Cmd
	if useLocalStoreDump != "" {
		dump = exec.CommandContext(ctx, common.NixBin+"-store", "--dump", useLocalStoreDump)
		if narOut, err = dump.StdoutPipe(); err != nil {
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
	} else {
		// start := time.Now()
		narUrl := upstreamUrl.JoinPath(ni.URL).String()
		res, err = http.Get(narUrl)
		if err != nil {
			return nil, fmt.Errorf("%w: nar http error for %s: %w", ErrReq, narUrl, err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%w: nar http status for %s: %s", ErrReq, narUrl, res.Status)
		}
		narOut = res.Body

		// log.Println("req", storePathHash, "downloading nar")

		switch ni.Compression {
		case "", "none":
			decompress = nil
		case "xz":
			decompress = exec.Command(common.XzBin, "-d")
		// case "zst":
		// TODO: use in-memory pipe?
		// 	decompress = exec.Command(common.ZstdBin, "-d")
		default:
			return nil, fmt.Errorf("%w: unknown compression for %s: %s", ErrReq, narUrl, ni.Compression)
		}
		if decompress != nil {
			decompress.Stdin = narOut
			narOut, err = decompress.StdoutPipe()
			if err != nil {
				return nil, fmt.Errorf("%w: can't create stdout pipe: %w", ErrInternal, err)
			}
			decompress.Stderr = os.Stderr
			if err = decompress.Start(); err != nil {
				return nil, fmt.Errorf("%w: nar decompress start error: %w", ErrInternal, err)
			}
			defer func() {
				if decompress != nil {
					decompress.Process.Kill()
					decompress.Wait()
				}
			}()
		}
	}

	// set up to hash nar

	narHasher, err := hash.New(ni.NarHash.HashType)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NarHashType: %w", ErrReq, err)
	}

	// TODO: make args configurable again (hashed in manifest cache key)
	args := &BuildArgs{
		SmallFileCutoff: DefaultSmallFileCutoff,
		ShardTotal:      shardTotal,
		ShardIndex:      shardIndex,
	}
	manifest, err := b.BuildFromNar(ctx, args, io.TeeReader(narOut, narHasher))
	if err != nil {
		return nil, fmt.Errorf("%w: manifest generation error: %w", ErrInternal, err)
	}

	// verify nar hash

	if narHasher.SRIString() != ni.NarHash.SRIString() {
		return nil, fmt.Errorf("%w: nar hash mismatch", ErrReq)
	}

	if decompress != nil {
		if err = decompress.Wait(); err != nil {
			return nil, fmt.Errorf("%w: nar decompress error: %w", ErrInternal, err)
		}
		decompress = nil
		// elapsed := time.Since(start)
		// ps := decompress.ProcessState
		// log.Printf("downloaded %s [%d bytes] in %s [decmp %s user, %s sys]: %.3f MB/s",
		// 	ni.URL, size, elapsed, ps.UserTime(), ps.SystemTime(),
		// 	float64(size)/elapsed.Seconds()/1e6)
	}

	if dump != nil {
		if err = dump.Wait(); err != nil {
			return nil, fmt.Errorf("%w: nar dump error: %w", ErrInternal, err)
		}
		dump = nil
	}
	log.Println("manifest", storePathHash, "built manifest")

	b.stats.Shards.Add(1)

	// if we're not shard 0, we're done
	if shardIndex != 0 {
		return nil, nil
	}

	// add metadata

	nipb := &pb.NarInfo{
		StorePath:   ni.StorePath,
		Url:         ni.URL,
		Compression: ni.Compression,
		FileHash:    ni.FileHash.NixString(),
		FileSize:    int64(ni.FileSize),
		NarHash:     ni.NarHash.NixString(),
		NarSize:     int64(ni.NarSize),
		References:  ni.References,
		Deriver:     ni.Deriver,
		System:      ni.System,
		Signatures:  make([]string, len(ni.Signatures)),
		Ca:          ni.CA,
	}
	for i, sig := range ni.Signatures {
		nipb.Signatures[i] = sig.String()
	}
	manifest.Meta = &pb.ManifestMeta{
		NarinfoUrl:    narinfoUrl,
		Narinfo:       nipb,
		Generator:     "styx-" + common.Version,
		GeneratedTime: time.Now().Unix(),
	}

	// turn into entry (maybe chunk)

	manifestArgs := BuildArgs{SmallFileCutoff: SmallManifestCutoff}
	path := common.ManifestContext + "/" + path.Base(ni.StorePath)
	entry, err := b.ManifestAsEntry(ctx, &manifestArgs, path, manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: make manifest entry error: %w", ErrInternal, err)
	}

	sb, err := common.SignMessageAsEntry(b.signKeys, b.params, entry)
	if err != nil {
		return nil, fmt.Errorf("%w: sign error: %w", ErrInternal, err)
	}

	// write to cache (it'd be nice to return and do this in the background, but that doesn't
	// work on lambda)
	// TODO: we shouldn't write to cache unless we know for sure that other shards are done.
	// (or else change client to re-request manifest on missing)
	cacheKey := (&ManifestReq{
		Upstream:      upstream,
		StorePathHash: storePathHash,
		ChunkShift:    int(common.ChunkShift),
		DigestAlgo:    common.DigestAlgo,
		DigestBits:    int(cdig.Bits),
	}).CacheKey()
	cmpSb, err := b.cs.PutIfNotExists(ctx, ManifestCachePath, cacheKey, sb)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest cache write error: %w", ErrInternal, err)
	}
	if cmpSb == nil {
		// already exists in cache, need to compress ourselves
		zp := common.GetZstdCtxPool()
		z := zp.Get()
		defer zp.Put(z)
		cmpSb, err = z.Compress(nil, sb)
		if err != nil {
			return nil, fmt.Errorf("%w: manifest compress error: %w", ErrInternal, err)
		}
	}
	log.Println("manifest", storePathHash, "added to cache")
	b.stats.Manifests.Add(1)
	return &ManifestBuildRes{
		CacheKey: cacheKey,
		Bytes:    cmpSb,
	}, nil
}

func (b *ManifestBuilder) BuildFromNar(ctx context.Context, args *BuildArgs, r io.Reader) (*pb.Manifest, error) {
	m := &pb.Manifest{
		Params:          b.params,
		SmallFileCutoff: int32(args.SmallFileCutoff),
	}

	nr, err := nar.NewReader(r)
	if err != nil {
		return nil, err
	}

	egCtx := errgroup.WithContext(ctx)
	for err == nil && egCtx.Err() == nil {
		err = b.entry(egCtx, args, m, nr)
	}
	if err == io.EOF {
		err = nil
	}

	return common.ValOrErr(m, cmp.Or(err, egCtx.Wait()))
}

func (b *ManifestBuilder) ManifestAsEntry(ctx context.Context, args *BuildArgs, path string, manifest *pb.Manifest) (*pb.Entry, error) {
	mb, err := proto.Marshal(manifest)
	if err != nil {
		return nil, err
	}

	entry := &pb.Entry{
		Path: path,
		Type: pb.EntryType_REGULAR,
		Size: int64(len(mb)),
	}

	if len(mb) <= args.SmallFileCutoff {
		entry.InlineData = mb
		return entry, nil
	}

	egCtx := errgroup.WithContext(ctx)
	entry.Digests, err = b.chunkData(egCtx, args, int64(len(mb)), bytes.NewReader(mb))

	return common.ValOrErr(entry, cmp.Or(err, egCtx.Wait()))
}

func (b *ManifestBuilder) entry(egCtx *errgroup.Group, args *BuildArgs, m *pb.Manifest, nr *nar.Reader) error {
	h, err := nr.Next()
	if err != nil { // including io.EOF
		return err
	} else if err = h.Validate(); err != nil {
		return err
	} else if h.Path == "/" && h.Type == nar.TypeSymlink {
		return errors.New("root can't be symlink")
	}

	e := &pb.Entry{
		Path:       h.Path,
		Executable: h.Executable,
		Size:       h.Size,
	}
	m.Entries = append(m.Entries, e)

	switch h.Type {
	case nar.TypeDirectory:
		e.Type = pb.EntryType_DIRECTORY

	case nar.TypeRegular:
		e.Type = pb.EntryType_REGULAR

		var dataR io.Reader = nr

		if e.Size <= int64(args.SmallFileCutoff) {
			e.InlineData = make([]byte, e.Size)
			if _, err := io.ReadFull(dataR, e.InlineData); err != nil {
				return err
			}
		} else {
			var err error
			e.Digests, err = b.chunkData(egCtx, args, e.Size, dataR)
			if err != nil {
				return err
			}
		}

	case nar.TypeSymlink:
		e.Type = pb.EntryType_SYMLINK
		e.InlineData = []byte(h.LinkTarget)

	default:
		return errors.New("unknown type")
	}

	return nil
}

// Note that goroutines will continue writing into the returned slice after this returns!
// Caller should not look at it until after Wait() on the errgroup.
func (b *ManifestBuilder) chunkData(egCtx *errgroup.Group, args *BuildArgs, dataSize int64, r io.Reader) ([]byte, error) {
	nChunks := common.ChunkShift.Blocks(dataSize)
	fullDigests := make([]byte, nChunks*cdig.Bytes)
	digests := fullDigests
	remaining := dataSize
	b.stats.TotalUncmpBytes.Add(dataSize)
	b.stats.TotalChunks.Add(nChunks)
	for remaining > 0 {
		if err := b.chunksem.Acquire(egCtx, 1); err != nil {
			return nil, err
		}

		size := min(remaining, common.ChunkShift.Size())
		remaining -= size
		_data := b.chunkPool.Get(int(size))
		data := _data[:size]

		if _, err := io.ReadFull(r, data); err != nil {
			b.chunkPool.Put(_data)
			b.chunksem.Release(1)
			return nil, err
		}
		digest := digests[:cdig.Bytes]
		digests = digests[cdig.Bytes:]

		// check shard
		putChunk := true
		if args.ShardTotal > 1 {
			putChunk = args.chunkIndex%args.ShardTotal == args.ShardIndex
			args.chunkIndex++
		}

		egCtx.Go(func() error {
			defer b.chunksem.Release(1)
			defer b.chunkPool.Put(_data)
			h := sha256.New()
			h.Write(data)
			var out [sha256.Size]byte
			copy(digest, h.Sum(out[0:0]))
			if !putChunk {
				return nil
			}
			compressed, err := b.cs.PutIfNotExists(egCtx, ChunkReadPath, cdig.FromBytes(digest).String(), data)
			if err != nil {
				return err
			}
			if compressed != nil {
				b.stats.NewChunks.Add(1)
				b.stats.NewUncmpBytes.Add(int64(len(data)))
				b.stats.NewCmpBytes.Add(int64(len(compressed)))
			}
			return nil
		})
	}

	return fullDigests, nil
}
