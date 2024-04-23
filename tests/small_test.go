package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSmallImage(t *testing.T) {
	tb := newTestBase(t)

	tb.startManifester()
	tb.startDaemon()

	// 144K package
	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))

	// TODO: get stats, read again, ensure everything was read directly from cache

	// try explicit unmount
	tb.umount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
}
