package devmapper

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRundup(t *testing.T) {
	require.Equal(t, 0, roundUp(0, 8))
	require.Equal(t, 8, roundUp(1, 8))
}

func TestFixedArrayToString(t *testing.T) {
	t.Parallel()

	check := func(input []byte, expected string) {
		str := fixedArrayToString(input)
		require.Equal(t, expected, str)
	}

	check([]byte{}, "")
	check([]byte{'r'}, "r")
	check([]byte{'h', 'e', 'l', 'l', 'o', ',', ' '}, "hello, ")
	check([]byte{'h', '\x00', 'l', 'l', 'o', ',', ' '}, "h")
	check([]byte{'\x00'}, "")
}
