package daemon

import (
	"math"
	"regexp"

	"github.com/nix-community/go-nix/pkg/storepath"
)

type (
	blkshift int
)

var reStorePath = regexp.MustCompile(`^[0123456789abcdfghijklmnpqrsvwxyz]{32}-.*$`)

func (b blkshift) size() int64 {
	return 1 << b
}

func (b blkshift) roundup(i int64) int64 {
	m1 := b.size() - 1
	return (i + m1) & ^m1
}

func (b blkshift) leftover(i int64) int64 {
	return i & (b.size() - 1)
}

func truncU16[L ~int | ~int32 | ~int64 | ~uint | ~uint16 | ~uint32 | ~uint64](v L) uint16 {
	if v < 0 || v > math.MaxUint16 {
		panic("overflow")
	}
	return uint16(v)
}

func truncU32[L ~int | ~int64 | ~uint | ~uint32 | ~uint64](v L) uint32 {
	if v < 0 || v > math.MaxUint32 {
		panic("overflow")
	}
	return uint32(v)
}

func valOrErr[T any](v T, err error) (T, error) {
	if err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}

func splitSphs(sphs []byte) [][]byte {
	out := make([][]byte, len(sphs)/storepath.PathHashSize)
	for i := range out {
		out[i] = sphs[i*storepath.PathHashSize : (i+1)*storepath.PathHashSize]
	}
	return out
}
