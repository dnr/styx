package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/daemon"
)

func TestRepeatedRead(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	mp1 := tb.mount("d30xd6x3669hg2a6xwjb1r3nb9a99sw2-openblas-0.3.27")
	bigFile := filepath.Join(mp1, "lib/libopenblasp-r0.3.27.so")
	require.Equal(t, "0q7zclw8sxfq5mvx0lf3clmqw31z9biq4adihcwh2hk6f39lia3w", tb.nixHash(bigFile))

	// reimplement some logic to figure out how many extra reads are expected (currently 1)
	size := 27720680
	extra := 0
	reqSize := daemon.InitOpSize
	for size > 0 {
		// FIXME: do we still need extra here?
		size -= reqSize << common.PickChunkShift(int64(size))
		extra += (reqSize - 1) / daemon.MaxOpSize
		reqSize = min(reqSize*2, daemon.MaxDiffOps*daemon.MaxOpSize)
	}
	require.Positive(t, extra, "file isn't big enough to have extra reads with current params")

	d1 := tb.debug()
	require.EqualValues(t, extra, d1.Stats.ExtraReqs)
}
