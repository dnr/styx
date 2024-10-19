package cdig

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
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

var ErrInvalid = errors.New("invalid base64 digest")

func Sum(b []byte) CDig {
	h := sha256.New()
	h.Write(b)
	var full [sha256.Size]byte
	return FromBytes(h.Sum(full[:0]))
}

func (dig CDig) String() string {
	return base64.RawURLEncoding.EncodeToString(dig[:])
}

func (dig CDig) Check(b []byte) error {
	if got := Sum(b); got != dig {
		return fmt.Errorf("chunk digest mismatch %x != %x", got, dig)
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

func FromBase64(s string) (dig CDig, err error) {
	src := unsafe.Slice(unsafe.StringData(s), len(s))
	var dst []byte
	dst, err = base64.RawURLEncoding.AppendDecode(dig[:0:Bytes], src)
	if err == nil && (&dst[0] != &dig[0] || len(dst) != Bytes) {
		err = ErrInvalid
	}
	return
}
