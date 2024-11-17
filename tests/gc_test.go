package tests

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dnr/styx/daemon"
)

func TestGc(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	// 144K package
	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	d1 := tb.debug(daemon.DebugReq{IncludeSlabs: true})
	require.Zero(t, d1.Slabs[0].Stats.PresentChunks)
	require.Zero(t, d1.Slabs[0].Stats.PresentBlocks)

	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))
	time.Sleep(200 * time.Millisecond) // batch delay
	d2 := tb.debug(daemon.DebugReq{IncludeSlabs: true})
	require.NotZero(t, d2.Slabs[0].Stats.PresentChunks)
	require.NotZero(t, d2.Slabs[0].Stats.PresentBlocks)

	gc1 := tb.gc()
	_ = gc1
}
