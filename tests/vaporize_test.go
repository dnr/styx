package tests

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVaporize(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	tmp := tb.t.TempDir()
	src1 := filepath.Join(tmp, "qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")

	err := exec.Command("sh", "-c", fmt.Sprintf(`
	xz -cd %s/nar/0h336qzb63kdqxwc5yjrxq61cjraz8jrav0m5rkrcvsb6w55rbll.nar.xz | nix-store --restore %s
`, TestdataDir, src1)).Run()
	require.NoError(t, err)

	tb.vaporize(src1)

	dst1 := tb.materialize("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(dst1))
	d1 := tb.debug()
	require.Zero(t, d1.Stats.SingleReqs+d1.Stats.BatchReqs+d1.Stats.DiffReqs)

	// mp2 := tb.materialize("kbi7qf642gsxiv51yqank8bnx39w3crd-calf-0.90.3")
	// require.Equal(t, "1bhyfn2k8w41cx7ddarmjmwscas0946n6gw5mralx9lg0vbbcx6d", tb.nixHash(mp2))

	// mp3 := tb.materialize("v35ysx9k1ln4c6r7lj74204ss4bw7l5l-openssl-3.0.12-man")
	// require.Equal(t, "0v60mg7qj7mfd27s1nnldb0041ln08xs1bw7zn1mmjiaq02myzlh", tb.nixHash(mp3))

	// mp4 := tb.materialize("3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch")
	// require.Equal(t, "13jlq14n974nn919530hnx4l46d0p2zyhx4lrd9b1k122dn7w9z5", tb.nixHash(mp4))

	// // for this one, mount then materialize, should use local manifest.
	// // this isn't a great test since materialize will fall back to remote manifest if error
	// // reading local, it'd be nice to disable that for this test.
	// _ = tb.mount("cd1nbildgzzfryjg82njnn36i4ynyf8h-bash-interactive-5.1-p16-man")
	// mp5 := tb.materialize("cd1nbildgzzfryjg82njnn36i4ynyf8h-bash-interactive-5.1-p16-man")
	// man1 := filepath.Join(mp5, "share", "man", "man1", "bash.1.gz")
	// require.Equal(t, "0s9d681f8smlsdvbp6lin9qrbsp3hz3dnf4pdhwi883v8l1486r7", tb.nixHash(man1))
}
