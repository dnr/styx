package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Daemon restart with mounted filesystems.
func TestRestart(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	// mount something with first daemon run
	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")

	// stop it. cachefiles fd will be saved in test fdstore
	tb.daemon.Stop(false)
	tb.daemon = nil

	// start again
	tb.startDaemon()

	// check that we can read
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))

	// this will fail if we didn't set up the slab read fd
	checkDiffAfterRestart(t, tb)
}

// Simulates reboot and starting with nothing mounted.
func TestReboot(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	// mount something with first daemon run
	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	mp2 := tb.mount("3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch")

	// read 2 but not 1
	require.Equal(t, "13jlq14n974nn919530hnx4l46d0p2zyhx4lrd9b1k122dn7w9z5", tb.nixHash(mp2))

	// stop and close devnode
	tb.daemon.Stop(true)
	tb.daemon = nil

	// unmount filesystems directly to simulate clean state after reboot
	// note that Stop unmounts the slab image
	require.NoError(t, unix.Unmount(mp1, 0))
	require.NoError(t, unix.Unmount(mp2, 0))

	// start again
	tb.startDaemon()

	// both should have been remounted automatically
	require.Equal(t, "13jlq14n974nn919530hnx4l46d0p2zyhx4lrd9b1k122dn7w9z5", tb.nixHash(mp2))

	// already cached, no requests
	d1 := tb.debug()
	require.Zero(t, d1.Stats.SingleReqs+d1.Stats.BatchReqs+d1.Stats.DiffReqs)

	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))

	// this needs some requests
	d2 := tb.debug()
	require.Zero(t, d2.Stats.SingleReqs+d2.Stats.DiffReqs)
	require.NotZero(t, d2.Stats.BatchReqs)

	// this will fail if we didn't set up the slab read fd
	checkDiffAfterRestart(t, tb)
}

func checkDiffAfterRestart(t *testing.T, tb *testBase) {
	mp := tb.mount("kcyrz2y8si9ry5p8qkmj0gp41n01sa1y-opusfile-0.12")
	d1 := tb.debug()
	require.Equal(t, "0im7spp48afrbfv672bmrvrs0lg4md0qhyic8zkcgyc8xqwz1s5b", tb.nixHash(mp))
	d2 := tb.debug()
	require.Zero(t, d2.Stats.SingleReqs-d1.Stats.SingleReqs)
	require.Zero(t, d2.Stats.BatchReqs-d1.Stats.BatchReqs)
	require.NotZero(t, d2.Stats.DiffReqs-d1.Stats.DiffReqs)
	require.Zero(t, d2.Stats.SingleErrs+d2.Stats.BatchErrs+d2.Stats.DiffErrs)
}
