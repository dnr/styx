package tests

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/dnr/styx/daemon"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func TestCloneSlabRollover(t *testing.T) {
	tb := newTestBase(t)
	// seed slab 0 as full, slab 1 will be created by allocation
	db, err := bbolt.Open(filepath.Join(tb.cachedir, "styx.bolt"), 0600, nil)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		root, err := tx.CreateBucketIfNotExists([]byte("slab"))
		if err != nil {
			return err
		}
		sb, err := root.CreateBucketIfNotExists([]byte{0, 0})
		if err != nil {
			return err
		}
		// slabBytes / block size, minus reserved blocks
		return sb.SetSequence((1<<40)>>blockShift - 16 - 1)
	}))
	require.NoError(t, db.Close())
	tb.startAll()

	// do it concurrently to check they share the clone dev
	storePaths := []string{
		"qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12",
		"kcyrz2y8si9ry5p8qkmj0gp41n01sa1y-opusfile-0.12",
	}
	mps := make([]string, len(storePaths))
	var wg sync.WaitGroup
	for i, sp := range storePaths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mps[i] = tb.mount(sp)
		}()
	}
	wg.Wait()
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mps[0]))
	require.Equal(t, "0im7spp48afrbfv672bmrvrs0lg4md0qhyic8zkcgyc8xqwz1s5b", tb.nixHash(mps[1]))
	d := tb.debug(daemon.DebugReq{IncludeAllChunks: true})
	require.NotEmpty(t, d.Chunks)
	for _, chunk := range d.Chunks {
		require.Equal(t, uint16(1), chunk.Slab)
	}
}
