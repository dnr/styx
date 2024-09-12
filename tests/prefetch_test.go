package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrefetch(t *testing.T) {
	tb := newTestBase(t)
	tb.startManifester()
	tb.startDaemon()

	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	sph := "qa22bifihaxyvn6q2a6w9m0nklqrk9wh"
	libDir := filepath.Join(mp1, "lib")
	shareDir := filepath.Join(mp1, "share")

	// nothing present yet
	// prefetch only share
	tb.prefetch(sph, "/share")
	d1 := tb.debug()
	require.Zero(t, d1.Stats.SingleReqs)
	require.EqualValues(t, 1, d1.Stats.BatchReqs) // fits in single req
	require.Zero(t, d1.Stats.DiffReqs)

	// read share dir, no reqs
	tb.nixHash(shareDir)
	d2 := tb.debug()
	require.Zero(t, d2.Stats.SingleReqs-d1.Stats.SingleReqs)
	require.Zero(t, d2.Stats.BatchReqs-d1.Stats.BatchReqs)
	require.Zero(t, d2.Stats.DiffReqs-d1.Stats.DiffReqs)

	// now prefetch lib
	tb.prefetch(sph, "/lib")
	d3 := tb.debug()
	require.Zero(t, d3.Stats.SingleReqs-d2.Stats.SingleReqs)
	require.EqualValues(t, 1, d3.Stats.BatchReqs-d2.Stats.BatchReqs) // fits in single req
	require.Zero(t, d3.Stats.DiffReqs-d2.Stats.DiffReqs)

	// read lib dir, no reqs
	tb.nixHash(libDir)
	d4 := tb.debug()
	require.Zero(t, d4.Stats.SingleReqs-d3.Stats.SingleReqs)
	require.Zero(t, d4.Stats.BatchReqs-d3.Stats.BatchReqs)
	require.Zero(t, d4.Stats.DiffReqs-d3.Stats.DiffReqs)

	// similar image
	mp2 := tb.mount("kcyrz2y8si9ry5p8qkmj0gp41n01sa1y-opusfile-0.12")
	sph2 := "kcyrz2y8si9ry5p8qkmj0gp41n01sa1y"

	// this one should do a diff (in one req)
	tb.prefetch(sph2, "/")
	d5 := tb.debug()
	require.Zero(t, d5.Stats.SingleReqs-d4.Stats.SingleReqs)
	require.Zero(t, d5.Stats.BatchReqs-d4.Stats.BatchReqs)
	require.EqualValues(t, 1, d5.Stats.DiffReqs-d4.Stats.DiffReqs)

	// read all, no reqs
	tb.nixHash(mp2)
	d6 := tb.debug()
	require.Zero(t, d6.Stats.SingleReqs-d5.Stats.SingleReqs)
	require.Zero(t, d6.Stats.BatchReqs-d5.Stats.BatchReqs)
	require.Zero(t, d6.Stats.DiffReqs-d5.Stats.DiffReqs)

	require.Zero(t, d6.Stats.SingleErrs+d6.Stats.BatchErrs+d6.Stats.DiffErrs)
}

func TestPrefetchLarge(t *testing.T) {
	tb := newTestBase(t)
	tb.startManifester()
	tb.startDaemon()

	mp1 := tb.mount("xpq4yhadyhazkcsggmqd7rsgvxb3kjy4-gnugrep-3.11")
	sph := "xpq4yhadyhazkcsggmqd7rsgvxb3kjy4"

	// 49 chunks total, but fits into one req because we increase the limit for prefetch
	tb.prefetch(sph, "/")
	d1 := tb.debug()
	require.Zero(t, d1.Stats.SingleReqs)
	require.EqualValues(t, 1, d1.Stats.BatchReqs)
	require.Zero(t, d1.Stats.DiffReqs)

	// read, no reqs
	tb.nixHash(mp1)
	d2 := tb.debug()
	require.Zero(t, d2.Stats.SingleReqs-d1.Stats.SingleReqs)
	require.Zero(t, d2.Stats.BatchReqs-d1.Stats.BatchReqs)
	require.Zero(t, d2.Stats.DiffReqs-d1.Stats.DiffReqs)
}
