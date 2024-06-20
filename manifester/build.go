package manifester

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"

	"github.com/nix-community/go-nix/pkg/nar"
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

		chunkIndex int
	}

	ManifestBuilder struct {
		cs          ChunkStoreWrite
		chunksem    *semaphore.Weighted
		params      *pb.GlobalParams
		chunk       common.BlkShift
		digestBytes int
		chunkPool   *common.ChunkPool
	}

	ManifestBuilderConfig struct {
		ConcurrentChunkOps int

		// global params
		ChunkShift int
		DigestAlgo string
		DigestBits int
	}
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
