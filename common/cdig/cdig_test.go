package cdig

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlice(t *testing.T) {
	b := make([]byte, Bytes*4)
	for i := range b {
		b[i] = byte(i)
	}
	d0, d1, d2, d3 := FromBytes(b[0:Bytes]), FromBytes(b[Bytes:Bytes*2]), FromBytes(b[Bytes*2:Bytes*3]), FromBytes(b[Bytes*3:])
	require.NotEqual(t, d0, d1)

	// This mostly tests that this aliasing works as expected.
	s1 := FromSliceAlias(b)
	require.Equal(t, 4, len(s1))
	require.Equal(t, d0, s1[0])
	require.Equal(t, d1, s1[1])
	require.Equal(t, d2, s1[2])
	require.Equal(t, d3, s1[3])

	s2 := make([]CDig, 4)
	copy(s2, s1)

	b2 := ToSliceAlias(s2)
	require.True(t, bytes.Equal(b2, b))
}
