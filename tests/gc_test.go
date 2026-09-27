package tests

import (
	"math/rand"
	"os"
	"testing"

	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/pb"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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
	hash3 := tb.nixHash(mp3)
	mp4 := tb.mount("xd96wmj058ky40aywv72z63vdw9yzzzb-openssl-3.0.12-man")
	tb.nixHash(mp4)
	mp5 := tb.mount("3a7xq2qhxw2r7naqmc53akmx7yvz0mkf-less-is-more.patch")
	tb.nixHash(mp5)

	// unmount 2 and 4
	tb.umount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh")
	tb.umount("xd96wmj058ky40aywv72z63vdw9yzzzb")

	debug := tb.debug(daemon.DebugReq{IncludeAllImages: true, IncludeSlabs: true})
	images := debug.Images
	slabBackingStores := make(map[uint32]string, len(debug.Slabs))
	for _, slab := range debug.Slabs {
		slabBackingStores[uint32(slab.Index)] = slab.BackingStore
	}
	imageFiles := make(map[uint32]*os.File)
	imageData := make(map[string][]byte)
	var imagePunchBytes int64
	for sp, di := range images {
		img := di.Image
		if imageFiles[img.ImageSlabId] == nil {
			backingStore := slabBackingStores[img.ImageSlabId]
			require.NotEmpty(t, backingStore, "missing backing store for image slab %d", img.ImageSlabId)
			f, err := os.Open(backingStore)
			require.NoError(t, err)
			t.Cleanup(func() { f.Close() })
			imageFiles[img.ImageSlabId] = f
		}
		buf := make([]byte, img.ImageBlockLength<<blockShift)
		_, err := imageFiles[img.ImageSlabId].ReadAt(buf, img.ImageBlockStart<<blockShift)
		require.NoError(t, err)
		imageData[sp] = buf
		if img.MountState == pb.MountState_Unmounted {
			imagePunchBytes += int64(len(buf))
		}
	}

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
	require.Equal(t, 5, gc2.PunchLocs)
	require.Equal(t, int64(4239360)+imagePunchBytes, gc2.PunchBytes)
	for sp, di := range images {
		buf := make([]byte, len(imageData[sp]))
		_, err := imageFiles[di.Image.ImageSlabId].ReadAt(buf, di.Image.ImageBlockStart<<blockShift)
		require.NoError(t, err)
		require.Equal(t, imageData[sp], buf, "dry run changed image %s", sp)
	}

	gc3 := tb.gc(daemon.GcReq{GcByState: gcUnmounted})
	t.Log("gc3:", gc3)
	require.Equal(t, gc2.PunchLocs, gc3.PunchLocs)
	require.Equal(t, gc2.PunchBytes, gc3.PunchBytes)
	for sp, di := range images {
		img := di.Image
		imageFile := imageFiles[img.ImageSlabId]
		offset := img.ImageBlockStart << blockShift
		buf := make([]byte, len(imageData[sp]))
		_, err := imageFile.ReadAt(buf, offset)
		require.NoError(t, err)
		if img.MountState == pb.MountState_Unmounted {
			require.Equal(t, make([]byte, len(buf)), buf)
			// verify it's really a hole
			requireFileHole(t, imageFile, offset, int64(len(buf)))
		} else {
			require.Equal(t, imageData[sp], buf, "GC changed retained image %s", sp)
		}
	}
	require.Zero(t, tb.gc(daemon.GcReq{GcByState: gcUnmounted}).PunchLocs)

	tb.dropCaches()

	// re-read remaining ones
	d1 := tb.debug()
	tb.nixHash(mp1)
	tb.nixHash(mp3)
	tb.nixHash(mp5)
	d2 := tb.debug()
	require.Zero(t, d2.Stats.Sub(d1.Stats).TotalReqs())

	// a gc'd image can be rebuilt and mounted again
	mp2 = tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp2))

	// a retained image can be remounted using its saved image
	tb.umount("8vyj9c6g424mz0v3kvzkskhvzhwj6288")
	mp3 = tb.mount("8vyj9c6g424mz0v3kvzkskhvzhwj6288-bash-interactive-5.2-p15-man")
	require.Equal(t, hash3, tb.nixHash(mp3))
}

// randomized test
func TestGcShared(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	fetch := []string{
		"cd1nbildgzzfryjg82njnn36i4ynyf8h-bash-interactive-5.1-p16-man",
		"8vyj9c6g424mz0v3kvzkskhvzhwj6288-bash-interactive-5.2-p15-man",
		"v35ysx9k1ln4c6r7lj74204ss4bw7l5l-openssl-3.0.12-man",
		"xd96wmj058ky40aywv72z63vdw9yzzzb-openssl-3.0.12-man",
		"z2waz77lsh4pxs0jxgmpf16s7a3g7b7v-openssl-3.0.13-man",
		"53qwclnym7a6vzs937jjmsfqxlxlsf2y-opusfile-0.12",
		"kcyrz2y8si9ry5p8qkmj0gp41n01sa1y-opusfile-0.12",
		"qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12",
	}
	// mount all
	mps := make([]string, len(fetch))
	for _, j := range rand.Perm(len(mps)) {
		mps[j] = tb.mount(fetch[j])
	}
	// read all (different order)
	for _, j := range rand.Perm(len(mps)) {
		tb.nixHash(mps[j])
	}
	// unmount half
	var remaining []string
	for i, j := range rand.Perm(len(mps)) {
		if i < len(mps)/2 {
			tb.umount(fetch[j])
		} else {
			remaining = append(remaining, mps[j])
		}
	}
	mps = remaining

	gc := tb.gc(daemon.GcReq{GcByState: gcUnmounted})
	t.Log("gc:", gc)

	unix.Sync()
	tb.dropCaches()
	unix.Sync()

	// re-read remaining ones
	d1 := tb.debug()
	for _, mp := range mps {
		tb.nixHash(mp)
	}
	d2 := tb.debug()
	// no errors, no requests
	require.Zero(t, d2.Stats.Sub(d1.Stats).TotalReqs())
}
