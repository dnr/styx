package manifester

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

	"github.com/dnr/styx/pb"
	"github.com/nix-community/go-nix/pkg/nar"
)

type (
	builderParams struct {
		chunkSize  int
		tailCutoff int
		hashBits   int
	}
)

func (s *server) buildManifest(r io.Reader, params builderParams) (*pb.Manifest, error) {
	m := &pb.Manifest{
		TailCutoff: int32(params.tailCutoff),
		ChunkSize:  int32(params.chunkSize),
		HashBits:   int32(params.hashBits),
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

			if h.Size <= int64(params.tailCutoff) {
				e.TailData = data
			} else {
				nChunks := (len(data) + params.chunkSize - 1) / params.chunkSize
				e.Digests = make([]byte, nChunks*(params.hashBits>>3))
				expected += nChunks
				for i := 0; i < nChunks; i++ {
					go s.handleChunk(params, data, e.Digests, i, errC)
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

func (s *server) handleChunk(params builderParams, data, digests []byte, i int, errC chan<- error) {
	// TODO: semaphore

	start := i * params.chunkSize
	end := min(start+params.chunkSize, len(data))
	chunk := data[start:end]

	hsh := sha256.New()
	hsh.Write(chunk)
	offset := i * params.hashBits >> 3
	digest := hsh.Sum(digests[offset:offset])
	digeststr := base64.RawURLEncoding.EncodeToString(digest)

	// TODO: look up digest in store, if present return
	// TODO: if not, compress + write to store

	errC <- nil
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
