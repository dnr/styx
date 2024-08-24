package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dnr/styx/daemon"
)

func TestChunkedManifest(t *testing.T) {
	tb := newTestBase(t)

	tb.startManifester()
	tb.startDaemon()

	// This is a relatively small package (5.7M) with a lot of files (5752), mostly symlinks.
	// Will be chunked manifest.
	mp1 := tb.mount("z2waz77lsh4pxs0jxgmpf16s7a3g7b7v-openssl-3.0.13-man")
	require.Equal(t, "1m9w6v5z6w73ii42xyfsgyckvl3zkk1bx5wzvsydd95jbfhz8aga", tb.nixHash(mp1))

	d1 := tb.debug()
	tb.dropCaches()
	require.Equal(t, "1m9w6v5z6w73ii42xyfsgyckvl3zkk1bx5wzvsydd95jbfhz8aga", tb.nixHash(mp1))
	d2 := tb.debug()
	require.Equal(t, d1.Stats.SlabReads, d2.Stats.SlabReads)

	// These should have very similar manifests, chunks should diff:
	mp2 := tb.mount("1fka6ngkrlmqkhix0gnnb19z58sr0yma-openssl-3.0.13-man")
	d3 := tb.debug()
	// just from mounting, we should have new diff reqs
	require.Greater(t, d3.Stats.DiffReqs, d2.Stats.DiffReqs)
	require.Zero(t, d3.Stats.DiffErrs)

	mp3 := tb.mount("xd96wmj058ky40aywv72z63vdw9yzzzb-openssl-3.0.12-man")
	d4 := tb.debug(daemon.DebugReq{IncludeSlabs: true})
	require.Greater(t, d4.Stats.DiffReqs, d3.Stats.DiffReqs)
	require.Zero(t, d4.Stats.DiffErrs)

	// Actually this one has identical contents, manifest chunks should be identical:
	mp4 := tb.mount("v35ysx9k1ln4c6r7lj74204ss4bw7l5l-openssl-3.0.12-man")
	d5 := tb.debug(daemon.DebugReq{IncludeSlabs: true})
	require.Equal(t, d4.Slabs[0].Stats.TotalChunks, d5.Slabs[0].Stats.TotalChunks)
	require.Equal(t, d4.Slabs[0].Stats.PresentChunks, d5.Slabs[0].Stats.PresentChunks)

	require.Equal(t, "1m9w6v5z6w73ii42xyfsgyckvl3zkk1bx5wzvsydd95jbfhz8aga", tb.nixHash(mp2))
	require.Equal(t, "0v60mg7qj7mfd27s1nnldb0041ln08xs1bw7zn1mmjiaq02myzlh", tb.nixHash(mp3))
	require.Equal(t, "0v60mg7qj7mfd27s1nnldb0041ln08xs1bw7zn1mmjiaq02myzlh", tb.nixHash(mp4))
}

// func TestNoOverflowBeforeSuper(t *testing.T) {
// 	tb := newTestBase(t)
// 	tb.startManifester()
// 	tb.startDaemon()
// 	mp1 := tb.mount("kbi7qf642gsxiv51yqank8bnx39w3crd-calf-0.90.3")
// 	require.Equal(t, "1bhyfn2k8w41cx7ddarmjmwscas0946n6gw5mralx9lg0vbbcx6d", tb.nixHash(mp1))
// }
