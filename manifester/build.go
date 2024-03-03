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
	BuildArgs struct {
		SmallFileCutoff int
	}

	ManifestBuilder struct {
		cs       ChunkStoreWrite
		chunksem *semaphore.Weighted
		chunkenc *zstd.Encoder
		params   *pb.GlobalParams
		chunk    blkshift
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
		chunk: blkshift(cfg.ChunkShift),
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

			// TODO: don't read full, stream this through with a concurrency limit on handleChunk
			data, err := readFullFromNar(nr, h)
			if err != nil {
				retErr = err
				break
			}

			if h.Size <= int64(args.SmallFileCutoff) {
				e.TailData = data
			} else {
				nChunks := (len(data) + int(b.chunk.size()) - 1) >> b.chunk
				e.Digests = make([]byte, nChunks*(int(b.params.DigestBits)>>3))
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

	start := i << b.chunk
	end := min(start+int(b.chunk.size()), len(data))
	chunk := data[start:end]

	h := sha256.New()
	h.Write(chunk)
	var out [sha256.Size]byte
	digest := h.Sum(out[0:0])[:b.params.DigestBits>>3]
	copy(digests[i*int(b.params.DigestBits)>>3:], digest)
	digeststr := base64.RawURLEncoding.EncodeToString(digest)

	errC <- b.cs.PutIfNotExists(ctx, digeststr, chunk)
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
