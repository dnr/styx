package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestart(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	// mount something with first daemon run
	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")

	// stop it. cachefiles fd will be saved in test fdstore
	tb.daemon.Stop(false)

	// start again
	tb.startDaemon()

	// check that we can read
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))
}
