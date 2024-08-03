package cdig

import (
	"encoding/base64"
	"unsafe"
)

const (
	Bytes = 24
	Bits  = Bytes << 3
)

type (
	CDig [Bytes]byte
)

var Zero CDig

func (dig CDig) String() string {
	return base64.RawURLEncoding.EncodeToString(dig[:])
}

// Note len(b) must be at least Bytes or this will panic.
func FromBytes(b []byte) (dig CDig) {
	copy(dig[:], b[:Bytes])
	return
}

// Views a byte CDig slice as a CDig slice. This aliases memory! Be careful.
func FromSliceAlias(b []byte) []CDig {
	p := unsafe.SliceData(b)
	dp := (*CDig)(unsafe.Pointer(p))
	return unsafe.Slice(dp, len(b)/Bytes)
}

// Views a CDig slice as a byte slice. This aliases memory! Be careful.
func ToSliceAlias(digests []CDig) []byte {
	p := unsafe.SliceData(digests)
	bp := (*byte)(unsafe.Pointer(p))
	return unsafe.Slice(bp, len(digests)*Bytes)
}
