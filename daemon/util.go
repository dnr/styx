package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/nix-community/go-nix/pkg/nixbase32"

	"github.com/dnr/styx/common"
)

// matches store path without the /nix/store
var reStorePath = regexp.MustCompile(`^[` + nixbase32.Alphabet + `]{32}-.*$`)

func checkChunkDigest(got, digest []byte) error {
	h := sha256.New() // TODO: support other hashes
	h.Write(got)
	var gotDigest [sha256.Size]byte
	if !bytes.Equal(h.Sum(gotDigest[:0])[:len(digest)], digest) {
		return fmt.Errorf("chunk digest mismatch %x != %x", gotDigest, digest)
	}
	return nil
}

func splitSphs(sphs []byte) []SphPrefix {
	out := make([]SphPrefix, len(sphs)/sphPrefixBytes)
	for i := range out {
		out[i] = SphPrefixFromBytes(sphs[i*sphPrefixBytes : (i+1)*sphPrefixBytes])
	}
	return out
}

func splitDigests(digests []byte, digestLen int) [][]byte {
	out := make([][]byte, len(digests)/digestLen)
	for i := range out {
		out[i] = digests[i*digestLen : (i+1)*digestLen]
	}
	return out
}

func writeToTempFile(b []byte) (string, error) {
	f, err := os.CreateTemp("", "styx-diff")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func makeManifestSph(sph Sph) Sph {
	// the "manifest sph" for a sph is the same with one bit flipped (will affect _end_ of base32
	// string form). note that this is its own inverse.
	sph[0] ^= 1
	return sph
}

func retryHttpRequest(ctx context.Context, method, url, cType string, body []byte) (*http.Response, error) {
	return retry.DoWithData(
		func() (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
			if err != nil {
				return nil, retry.Unrecoverable(err)
			}
			req.Header.Set("Content-Type", cType)
			res, err := http.DefaultClient.Do(req)
			if err == nil && res.StatusCode != http.StatusOK {
				err = common.HttpError(res.StatusCode)
				res.Body.Close()
			}
			return common.ValOrErr(res, err)
		},
		retry.Context(ctx),
		retry.UntilSucceeded(),
		retry.Delay(time.Second),
		retry.RetryIf(func(err error) bool {
			// retry on err or some 50x codes
			if status, ok := err.(common.HttpError); ok {
				switch status {
				case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
					return true
				default:
					return false
				}
			} else if common.IsContextError(err) {
				return false
			}
			return true
		}),
		retry.OnRetry(func(n uint, err error) {
			log.Printf("http error (%d): %v, retrying", n, err)
		}))
}

type countReader struct {
	r io.Reader
	c int64
}

func (cr *countReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.c += int64(n)
	return n, err
}

func underDir(p, dir string) bool {
	return len(p) >= len(dir) && p[:len(dir)] == dir && (len(p) == len(dir) || dir == "/" || p[len(dir)] == '/')
}
