package cdig

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"unsafe"
)

const (
	Bytes = 24
	Bits  = Bytes << 3
	Algo  = "sha256"
)

type (
	CDig [Bytes]byte
)

var Zero CDig

func (dig CDig) String() string {
	return base64.RawURLEncoding.EncodeToString(dig[:])
}

func (dig CDig) Check(b []byte) error {
	h := sha256.New()
	h.Write(b)
	var full [sha256.Size]byte
	if FromBytes(h.Sum(full[:0])) != dig {
		return fmt.Errorf("chunk digest mismatch %x != %x", full, dig)
	}
	return nil
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
