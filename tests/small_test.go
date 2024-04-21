package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// mount small image with inline manifest
// check metadata
// check data
// unmount
func TestSmallImage(t *testing.T) {
	tb := newTestBase(t)

	tb.startManifester()
	tb.startDaemon()

	mp := tb.mount("jc774xyapmj56icn80q9h60nhavxc272-ripgrep-13.0.0")
	require.Equal(t, "0f7f5vqizf9inqn51c0mpi7l3zv1k64w4xwgldivbq0mhxf4spxx", tb.nixHash(mp))
}
