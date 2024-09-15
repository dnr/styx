package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBare(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	// bare file package
	mp1 := tb.mount("3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch")
	require.Equal(t, "13jlq14n974nn919530hnx4l46d0p2zyhx4lrd9b1k122dn7w9z5", tb.nixHash(mp1))
}
