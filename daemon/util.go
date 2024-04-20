package daemon

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"regexp"

	"github.com/nix-community/go-nix/pkg/storepath"
)

var reStorePath = regexp.MustCompile(`^[0123456789abcdfghijklmnpqrsvwxyz]{32}-.*$`)

func checkChunkDigest(got, digest []byte) error {
	h := sha256.New() // TODO: support other hashes
	h.Write(got)
	var gotDigest [sha256.Size]byte
	if !bytes.Equal(h.Sum(gotDigest[:0])[:len(digest)], digest) {
		return fmt.Errorf("chunk digest mismatch %x != %x", gotDigest, digest)
	}
	return nil
}

func splitSphs(sphs []byte) []Sph {
	out := make([]Sph, len(sphs)/storepath.PathHashSize)
	for i := range out {
		var sph Sph
		copy(sph[:], sphs[i*storepath.PathHashSize:(i+1)*storepath.PathHashSize])
		out[i] = sph
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
	// the "manifest sph" for a sph is the same with the second half of bits flipped
	for i := len(sph) / 2; i < len(sph); i++ {
		sph[i] ^= 0xff
	}
}
