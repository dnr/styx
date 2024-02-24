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
	"golang.org/x/sync/semaphore"
)

type (
	builderParams struct {
		chunkSize  int
		tailCutoff int
		hashBits   int
	}

	ManifestBuilder struct {
		cs       ChunkStore
		chunksem *semaphore.Weighted
		chunkenc *zstd.Encoder
		params   builderParams
	}

	ManifestBuilderConfig struct {
		ConcurrentChunkOps int
	}
)

func NewManifestBuilder(cfg ManifestBuilderConfig, cs ChunkStore) (*ManifestBuilder, error) {
	chunkenc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderCRC(false),
	)
	if err != nil {
		return nil, err
	}
	return &ManifestBuilder{
		cs:       cs,
		chunksem: semaphore.NewWeighted(int64(Or(cfg.ConcurrentChunkOps, 50))),
		chunkenc: chunkenc,
		params: builderParams{
			tailCutoff: defaultTailCutoff,
			chunkSize:  defaultChunkSize,
			hashBits:   defaultHashBits,
		},
	}, nil
}

func (b *ManifestBuilder) Build(ctx context.Context, r io.Reader) (*pb.Manifest, error) {
	m := &pb.Manifest{
		TailCutoff: int32(b.params.tailCutoff),
		ChunkSize:  int32(b.params.chunkSize),
		HashBits:   int32(b.params.hashBits),
	}

	nr, err := nar.NewReader(r)
	if err != nil {
		return nil, err
	}

	errC := make(chan error, 100)
	expected := 0
	var retErr error

	for {
		h, err := nr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			retErr = err
			break
		} else if err = h.Validate(); err != nil {
			retErr = err
			break
		} else if h.Path == "/" && h.Type != nar.TypeDirectory {
			retErr = errors.New("can't handle bare file nars yet")
			break
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

			data, err := readFullFromNar(nr, h)
			if err != nil {
				retErr = err
				break
			}

			if h.Size <= int64(b.params.tailCutoff) {
				e.TailData = data
			} else {
				nChunks := (len(data) + b.params.chunkSize - 1) / b.params.chunkSize
				e.Digests = make([]byte, nChunks*(b.params.hashBits>>3))
				expected += nChunks
				for i := 0; i < nChunks; i++ {
					go b.handleChunk(ctx, data, e.Digests, i, errC)
				}
			}

		case nar.TypeSymlink:
			e.Type = pb.EntryType_SYMLINK
			e.TailData = []byte(h.LinkTarget)

		default:
			retErr = errors.New("unknown type")
			break
		}
	}

	for i := 0; i < expected; i++ {
		err := <-errC
		if err != nil && retErr == nil {
			retErr = err
		}
	}
	if retErr != nil {
		return nil, retErr
	}

	return m, nil
}

func (b *ManifestBuilder) handleChunk(ctx context.Context, data, digests []byte, i int, errC chan<- error) {
	b.chunksem.Acquire(ctx, 1)
	defer b.chunksem.Release(1)

	start := i * b.params.chunkSize
	end := min(start+b.params.chunkSize, len(data))
	chunk := data[start:end]

	h := sha256.New()
	h.Write(chunk)
	var out [sha256.Size]byte
	digest := h.Sum(out[0:0])[:b.params.hashBits>>3]
	copy(digests[i*b.params.hashBits>>3:], digest)
	digeststr := base64.RawURLEncoding.EncodeToString(digest)

	errC <- b.cs.PutIfNotExists(ctx, digeststr, func() []byte {
		return b.chunkenc.EncodeAll(chunk, nil)
	})
}

func readFullFromNar(nr *nar.Reader, h *nar.Header) ([]byte, error) {
	buf := make([]byte, h.Size)
	num, err := io.ReadFull(nr, buf)
	if err != nil {
		return nil, err
	} else if num != int(h.Size) {
		return nil, io.ErrUnexpectedEOF
	}
	return buf, nil
}
