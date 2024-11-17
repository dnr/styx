package tests

import (
	"testing"

	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/pb"
	"github.com/stretchr/testify/require"
)

var gcUnmounted = map[pb.MountState]bool{
	pb.MountState_Unmounted: true,
}

func TestGc(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	mp1 := tb.mount("xpq4yhadyhazkcsggmqd7rsgvxb3kjy4-gnugrep-3.11")
	tb.nixHash(mp1)
	mp2 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	tb.nixHash(mp2)
	mp3 := tb.mount("8vyj9c6g424mz0v3kvzkskhvzhwj6288-bash-interactive-5.2-p15-man")
	tb.nixHash(mp3)
	mp4 := tb.mount("xd96wmj058ky40aywv72z63vdw9yzzzb-openssl-3.0.12-man")
	tb.nixHash(mp4)
	mp5 := tb.mount("3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch")
	tb.nixHash(mp5)

	// unmount 2 and 4
	tb.umount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh")
	tb.umount("xd96wmj058ky40aywv72z63vdw9yzzzb")

	gc1 := tb.gc(daemon.GcReq{DryRunFast: true, GcByState: gcUnmounted})
	t.Log("gc1:", gc1)
	require.Equal(t, 2, gc1.DeleteImages)
	require.Equal(t, 3, gc1.RemainImages)
	require.Equal(t, 806, gc1.DeleteChunks)
	require.Equal(t, 53, gc1.RemainRefChunks)
	require.Equal(t, 53, gc1.RemainHaveChunks)
	require.Equal(t, 0, gc1.RewriteChunks)

	gc2 := tb.gc(daemon.GcReq{DryRunSlow: true, GcByState: gcUnmounted})
	t.Log("gc2:", gc2)
	require.Equal(t, 3, gc2.PunchLocs)
	require.Equal(t, int64(4239360), gc2.PunchBytes)

	gc3 := tb.gc(daemon.GcReq{GcByState: gcUnmounted})
	t.Log("gc3:", gc3)
	require.Equal(t, int64(4239360), gc3.PunchBytes)

	tb.dropCaches()

	// re-read remaining ones
	d1 := tb.debug()
	tb.nixHash(mp1)
	tb.nixHash(mp3)
	tb.nixHash(mp5)
	d2 := tb.debug()
	require.Zero(t, d2.Stats.Sub(d1.Stats).TotalReqs())
}
