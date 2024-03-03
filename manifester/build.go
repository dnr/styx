package manifester

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"github.com/dnr/styx/pb"
	"github.com/klauspost/compress/zstd"
	"github.com/nix-community/go-nix/pkg/nar"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

type (
	BuildArgs struct {
		SmallFileCutoff int
	}

	ManifestBuilder struct {
		cs          ChunkStoreWrite
		chunksem    *semaphore.Weighted
		chunkenc    *zstd.Encoder
		params      *pb.GlobalParams
		chunk       blkshift
		digestBytes int
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
		chunksem: semaphore.NewWeighted(int64(Or(cfg.ConcurrentChunkOps, 50))),
		params: &pb.GlobalParams{
			ChunkShift: int32(cfg.ChunkShift),
			DigestAlgo: cfg.DigestAlgo,
			DigestBits: int32(cfg.DigestBits),
		},
		chunk:       blkshift(cfg.ChunkShift),
		digestBytes: cfg.DigestBits >> 3,
	}, nil
}

func (b *ManifestBuilder) Build(ctx context.Context, args BuildArgs, r io.Reader) (*pb.Manifest, error) {
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

	err = Or(err, eg.Wait())

	if err != nil {
		return nil, err
	}
	return m, nil
}

func (b *ManifestBuilder) entry(ctx context.Context, args BuildArgs, m *pb.Manifest, nr *nar.Reader, eg *errgroup.Group) error {
	h, err := nr.Next()
	if err != nil { // including io.EOF
		return err
	} else if err = h.Validate(); err != nil {
		return err
	} else if h.Path == "/" && h.Type != nar.TypeDirectory {
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

		if h.Size <= int64(args.SmallFileCutoff) {
			data := make([]byte, h.Size)
			if _, err := io.ReadFull(nr, data); err != nil {
				return err
			}
			e.TailData = data
		} else {
			nChunks := int((h.Size + b.chunk.size() - 1) >> b.chunk)
			digests := make([]byte, nChunks*b.digestBytes)
			e.Digests = digests
			remaining := h.Size
			for remaining > 0 {
				b.chunksem.Acquire(ctx, 1)
				data := make([]byte, min(remaining, b.chunk.size()))
				remaining -= int64(len(data))
				if _, err := io.ReadFull(nr, data); err != nil {
					b.chunksem.Release(1)
					return err
				}
				digest := digests[:b.digestBytes]
				digests = digests[b.digestBytes:]
				eg.Go(func() error {
					defer b.chunksem.Release(1)
					h := sha256.New()
					h.Write(data)
					var out [sha256.Size]byte
					copy(digest, h.Sum(out[0:0]))
					digeststr := base64.RawURLEncoding.EncodeToString(digest)
					return b.cs.PutIfNotExists(ctx, digeststr, data)
				})
			}
		}

	case nar.TypeSymlink:
		e.Type = pb.EntryType_SYMLINK
		e.TailData = []byte(h.LinkTarget)

	default:
		return errors.New("unknown type")
	}

	return nil
}
