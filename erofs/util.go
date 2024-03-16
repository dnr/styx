package erofs

import (
	"math"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type (
	blkshift int
)

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

func truncU8[L ~int | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64](v L) uint8 {
	if v < 0 || v > math.MaxUint8 {
		panic("overflow")
	}
	return uint8(v)
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

func truncU64[L ~int | ~int64 | ~uint | ~uint64](v L) uint64 {
	if v < 0 {
		panic("overflow")
	}
	return uint64(v)
}

// I don't know why I can't write "proto.Message" in here, but expanding it works
func unmarshalAs[T any, PM interface {
	ProtoReflect() protoreflect.Message
	*T
}](b []byte) (*T, error) {
	var m PM = new(T)
	return valOrErr(m, proto.Unmarshal(b, m))
}

func valOrErr[T any](v T, err error) (T, error) {
	if err != nil {
		var zero T
		return zero, err
	}
	return v, nil
}
