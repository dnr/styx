package daemon

import (
	"math"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

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

// I don't know why I can't write "proto.Message" in here, but expanding it works
func unmarshalAs[T any, PM interface {
	ProtoReflect() protoreflect.Message
	*T
}](b []byte) (*T, error) {
	var m PM = new(T)
	if err := proto.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
}
