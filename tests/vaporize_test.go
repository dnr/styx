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

	testVaporize := func(name, filehash, datahash string, expected int64, doMount bool) {
		d1 := tb.debug()
		src := filepath.Join(tmp, name)
		cmd := fmt.Sprintf(` xz -cd %s/nar/%s.nar.xz | nix-store --restore %s `, TestdataDir, filehash, src)
		require.NoError(t, exec.Command("sh", "-c", cmd).Run())
		// vaporize into slab
		tb.vaporize(src)
		// now try to materialize out of slab
		var dst string
		if doMount {
			dst = tb.mount(name)
		} else {
			dst = tb.materialize(name)
		}
		require.Equal(t, datahash, tb.nixHash(dst))
		// should be requests for manifest chunks only, all data is in slab
		d2 := tb.debug()
		require.Equal(t, expected,
			(d2.Stats.SingleReqs+d2.Stats.BatchReqs+d2.Stats.DiffReqs)-
				(d1.Stats.SingleReqs+d1.Stats.BatchReqs+d1.Stats.DiffReqs))
		// TODO: btrfs fi du to check that extents are shared
	}

	testVaporize(
		"qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12",
		"0h336qzb63kdqxwc5yjrxq61cjraz8jrav0m5rkrcvsb6w55rbll",
		"1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm",
		0,
		false)

	testVaporize(
		"kbi7qf642gsxiv51yqank8bnx39w3crd-calf-0.90.3",
		"11r77sgdyqqfi8z36p104098g16cvvq6jvhypv0xw79jrqq33j7n",
		"1bhyfn2k8w41cx7ddarmjmwscas0946n6gw5mralx9lg0vbbcx6d",
		0, // 1 manifest chunk. TODO: why doesn't this need a manifest chunk anymore?
		false)

	testVaporize(
		"v35ysx9k1ln4c6r7lj74204ss4bw7l5l-openssl-3.0.12-man",
		"1mv76iwv027rxgdb0i04www6nkx8hy5bxh8v8vjihr9pl5a37hpy",
		"0v60mg7qj7mfd27s1nnldb0041ln08xs1bw7zn1mmjiaq02myzlh",
		1, // 1 manifest chunk
		true)

	testVaporize(
		"3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch",
		"0a2jm36lynlaw4vxr4xnflxz93jadr4xw03ab2hgardshqij3y7c",
		"13jlq14n974nn919530hnx4l46d0p2zyhx4lrd9b1k122dn7w9z5",
		0,
		false)

	testVaporize(
		"cd1nbildgzzfryjg82njnn36i4ynyf8h-bash-interactive-5.1-p16-man",
		"0r02jy5bmdl5gvflmm9yq3aqa12zbv4hkkv1lqhm6ps49xnxlq6c",
		"0anwmd9q85lyn4aid5glvzf5ikwr113zbrrpym1nf377r0ap298s",
		0,
		true)
}
