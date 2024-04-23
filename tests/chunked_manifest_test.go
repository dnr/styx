package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChunkedManifest(t *testing.T) {
	tb := newTestBase(t)

	tb.startManifester()
	tb.startDaemon()

	// This is a relatively small package (5.7M) with a lot of files (5752), mostly symlinks.
	// Will be chunked manifest.
	mp1 := tb.mount("z2waz77lsh4pxs0jxgmpf16s7a3g7b7v-openssl-3.0.13-man")
	require.Equal(t, "1m9w6v5z6w73ii42xyfsgyckvl3zkk1bx5wzvsydd95jbfhz8aga", tb.nixHash(mp1))

	// TODO: get stats, read again, ensure everything was read directly from cache

	// These should have very similar manifests, chunks should diff:
	// TODO: verify that chunks diffed
	mp2 := tb.mount("1fka6ngkrlmqkhix0gnnb19z58sr0yma-openssl-3.0.13-man")
	mp3 := tb.mount("xd96wmj058ky40aywv72z63vdw9yzzzb-openssl-3.0.12-man")
	// Actually this one has identical contents, manifest chunks should be identical:
	// TODO: verify that no new chunks were downloaded
	mp4 := tb.mount("v35ysx9k1ln4c6r7lj74204ss4bw7l5l-openssl-3.0.12-man")

	_ = mp2 + mp3 + mp4
	// require.Equal(t, "1m9w6v5z6w73ii42xyfsgyckvl3zkk1bx5wzvsydd95jbfhz8aga", tb.nixHash(mp2))
	// require.Equal(t, "0v60mg7qj7mfd27s1nnldb0041ln08xs1bw7zn1mmjiaq02myzlh", tb.nixHash(mp3))
	// require.Equal(t, "0v60mg7qj7mfd27s1nnldb0041ln08xs1bw7zn1mmjiaq02myzlh", tb.nixHash(mp4))
}
