package manifester

import (
	"bytes"
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
	"time"

	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
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
		cs          ChunkStoreWrite
		chunksem    *semaphore.Weighted
		params      *pb.GlobalParams
		chunk       common.BlkShift
		digestBytes int
		chunkPool   *common.ChunkPool
		pubKeys     []signature.PublicKey
		signKeys    []signature.SecretKey
	}

	ManifestBuilderConfig struct {
		ConcurrentChunkOps int

		// global params
		ChunkShift int
		DigestAlgo string
		DigestBits int

		// Verify loaded narinfo against these keys. Nil means don't verify.
		PublicKeys []signature.PublicKey
		// Sign manifests with these keys.
		SigningKeys []signature.SecretKey
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
		chunksem: semaphore.NewWeighted(int64(common.Or(cfg.ConcurrentChunkOps, 200))),
		params: &pb.GlobalParams{
			ChunkShift: int32(cfg.ChunkShift),
			DigestAlgo: cfg.DigestAlgo,
			DigestBits: int32(cfg.DigestBits),
		},
		chunk:       common.BlkShift(cfg.ChunkShift),
		digestBytes: cfg.DigestBits >> 3,
		chunkPool:   common.NewChunkPool(cfg.ChunkShift),
		pubKeys:     cfg.PublicKeys,
		signKeys:    cfg.SigningKeys,
	}, nil
}

func (b *ManifestBuilder) Build(
	ctx context.Context,
	upstream, storePathHash string,
	shardTotal, shardIndex int,
	useLocalStoreDump string,
) ([]byte, error) {
	// get narinfo

	// FIXME: consolidate url parsing?
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

	log.Println("req", storePathHash, "got narinfo", ni.StorePath[44:], ni.FileSize, ni.NarSize)

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
		defer dump.Wait()
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
		}
	}

	// set up to hash nar

	narHasher, err := hash.New(ni.NarHash.HashType)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NarHashType: %w", ErrReq, err)
	}

	// TODO: make args configurable again (hashed in manifest cache key)
	args := &BuildArgs{
		SmallFileCutoff: defaultSmallFileCutoff,
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
	}

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

	manifestArgs := BuildArgs{SmallFileCutoff: smallManifestCutoff}
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
		ChunkShift:    int(b.params.ChunkShift),
		DigestAlgo:    b.params.DigestAlgo,
		DigestBits:    int(b.params.DigestBits),
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
	return cmpSb, nil
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

	eg, gCtx := errgroup.WithContext(ctx)
	for err == nil && gCtx.Err() == nil {
		err = b.entry(gCtx, args, m, nr, eg)
	}
	if err == io.EOF {
		err = nil
	}

	return common.ValOrErr(m, common.Or(err, eg.Wait()))
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

	eg, gCtx := errgroup.WithContext(ctx)
	entry.Digests, err = b.chunkData(gCtx, args, int64(len(mb)), bytes.NewReader(mb), eg)

	return common.ValOrErr(entry, common.Or(err, eg.Wait()))
}

func (b *ManifestBuilder) entry(ctx context.Context, args *BuildArgs, m *pb.Manifest, nr *nar.Reader, eg *errgroup.Group) error {
	h, err := nr.Next()
	if err != nil { // including io.EOF
		return err
	} else if err = h.Validate(); err != nil {
		return err
	} else if h.Path == "/" && h.Type != nar.TypeDirectory {
		// TODO: allow these in manifests, use bind mounts in daemon
		return errors.New("can't handle bare file nars yet")
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
			e.Digests, err = b.chunkData(ctx, args, e.Size, dataR, eg)
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

func (b *ManifestBuilder) chunkData(ctx context.Context, args *BuildArgs, dataSize int64, r io.Reader, eg *errgroup.Group) ([]byte, error) {
	nChunks := int((dataSize + b.chunk.Size() - 1) >> b.chunk)
	fullDigests := make([]byte, nChunks*b.digestBytes)
	digests := fullDigests
	remaining := dataSize
	for remaining > 0 {
		b.chunksem.Acquire(ctx, 1)

		size := min(remaining, b.chunk.Size())
		remaining -= size
		_data := b.chunkPool.Get(int(size))
		data := _data[:size]

		if _, err := io.ReadFull(r, data); err != nil {
			b.chunkPool.Put(_data)
			b.chunksem.Release(1)
			return nil, err
		}
		digest := digests[:b.digestBytes]
		digests = digests[b.digestBytes:]

		// check shard
		putChunk := true
		if args.ShardTotal > 1 {
			putChunk = args.chunkIndex%args.ShardTotal == args.ShardIndex
			args.chunkIndex++
		}

		eg.Go(func() error {
			defer b.chunksem.Release(1)
			defer b.chunkPool.Put(_data)
			h := sha256.New()
			h.Write(data)
			var out [sha256.Size]byte
			copy(digest, h.Sum(out[0:0]))
			if !putChunk {
				return nil
			}
			_, err := b.cs.PutIfNotExists(ctx, ChunkReadPath, common.DigestStr(digest), data)
			return err
		})
	}

	return fullDigests, nil
}
