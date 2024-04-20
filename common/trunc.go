package common

import "math"

func TruncU8[L ~int | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64](v L) uint8 {
	if v < 0 || v > math.MaxUint8 {
		panic("overflow")
	}
	return uint8(v)
}

func TruncU16[L ~int | ~int32 | ~int64 | ~uint | ~uint16 | ~uint32 | ~uint64](v L) uint16 {
	if v < 0 || v > math.MaxUint16 {
		panic("overflow")
	}
	return uint16(v)
}

func TruncU32[L ~int | ~int64 | ~uint | ~uint32 | ~uint64](v L) uint32 {
	if v < 0 || v > math.MaxUint32 {
		panic("overflow")
	}
	return uint32(v)
}

func TruncU64[L ~int | ~int64 | ~uint | ~uint64](v L) uint64 {
	if v < 0 {
		panic("overflow")
	}
	return uint64(v)
}
