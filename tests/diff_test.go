package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiffChunks(t *testing.T) {
	t.Skip("diff not working yet")

	tb := newTestBase(t)

	tb.startManifester()
	tb.startDaemon()

	// these are very similar 144K packages so should diff well
	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))

	mp2 := tb.mount("kcyrz2y8si9ry5p8qkmj0gp41n01sa1y-opusfile-0.12")
	require.Equal(t, "0im7spp48afrbfv672bmrvrs0lg4md0qhyic8zkcgyc8xqwz1s5b", tb.nixHash(mp2))

	mp3 := tb.mount("53qwclnym7a6vzs937jjmsfqxlxlsf2y-opusfile-0.12")
	require.Equal(t, "0dm2277wfknq81wfwzxrasc9rif30fm03vxahndbqnn4gb9swqpq", tb.nixHash(mp3))

	// TODO: check that the chunks were actually diffed
}
