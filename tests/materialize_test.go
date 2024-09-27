package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaterialize(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	mp1 := tb.materialize("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))
	// TODO: check btrfs fi du

	mp2 := tb.materialize("kbi7qf642gsxiv51yqank8bnx39w3crd-calf-0.90.3")
	require.Equal(t, "1bhyfn2k8w41cx7ddarmjmwscas0946n6gw5mralx9lg0vbbcx6d", tb.nixHash(mp2))

	mp3 := tb.materialize("v35ysx9k1ln4c6r7lj74204ss4bw7l5l-openssl-3.0.12-man")
	require.Equal(t, "0v60mg7qj7mfd27s1nnldb0041ln08xs1bw7zn1mmjiaq02myzlh", tb.nixHash(mp3))

	// TODO: test bare file
	// mp4 := tb.materialize("3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch")
	// require.Equal(t, "13jlq14n974nn919530hnx4l46d0p2zyhx4lrd9b1k122dn7w9z5", tb.nixHash(mp4))

	mp5 := tb.materialize("cd1nbildgzzfryjg82njnn36i4ynyf8h-bash-interactive-5.1-p16-man")
	man1 := filepath.Join(mp5, "share", "man", "man1", "bash.1.gz")
	require.Equal(t, "0s9d681f8smlsdvbp6lin9qrbsp3hz3dnf4pdhwi883v8l1486r7", tb.nixHash(man1))

	mp6 := tb.materialize("d30xd6x3669hg2a6xwjb1r3nb9a99sw2-openblas-0.3.27")
	bigFile := filepath.Join(mp6, "lib/libopenblasp-r0.3.27.so")
	require.Equal(t, "0q7zclw8sxfq5mvx0lf3clmqw31z9biq4adihcwh2hk6f39lia3w", tb.nixHash(bigFile))
}
