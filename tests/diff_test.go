package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiffChunks(t *testing.T) {
	tb := newTestBase(t)

	tb.startManifester()
	tb.startDaemon()

	// these are very similar 144K packages so should diff well
	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))
	d1 := tb.debug()
	require.Zero(t, d1.Stats.SingleReqs)
	require.NotZero(t, d1.Stats.BatchReqs)
	require.Zero(t, d1.Stats.DiffReqs)

	mp2 := tb.mount("kcyrz2y8si9ry5p8qkmj0gp41n01sa1y-opusfile-0.12")
	require.Equal(t, "0im7spp48afrbfv672bmrvrs0lg4md0qhyic8zkcgyc8xqwz1s5b", tb.nixHash(mp2))
	d2 := tb.debug()
	require.Zero(t, d2.Stats.SingleReqs-d1.Stats.SingleReqs)
	require.Zero(t, d2.Stats.BatchReqs-d1.Stats.BatchReqs)
	require.NotZero(t, d2.Stats.DiffReqs-d1.Stats.DiffReqs)

	mp3 := tb.mount("53qwclnym7a6vzs937jjmsfqxlxlsf2y-opusfile-0.12")
	require.Equal(t, "0dm2277wfknq81wfwzxrasc9rif30fm03vxahndbqnn4gb9swqpq", tb.nixHash(mp3))
	d3 := tb.debug()
	require.Zero(t, d3.Stats.SingleReqs-d2.Stats.SingleReqs)
	require.Zero(t, d3.Stats.BatchReqs-d2.Stats.BatchReqs)
	require.NotZero(t, d3.Stats.DiffReqs-d2.Stats.DiffReqs)

	require.Zero(t, d3.Stats.SingleErrs+d3.Stats.BatchErrs+d3.Stats.DiffErrs)
}
