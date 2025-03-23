package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemanifestOnNotFound(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	mp1 := tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")

	// wipe chunk store
	ents, err := os.ReadDir(tb.chunkdir)
	require.NoError(t, err)
	for _, ent := range ents {
		os.Remove(filepath.Join(tb.chunkdir, ent.Name()))
	}

	// should succeed anyway
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))
}

func TestRemanifestPrefetch(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	// mount to manifest and get chunks in cache, but not locally
	tb.mount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	tb.umount("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")

	// wipe chunk store
	ents, err := os.ReadDir(tb.chunkdir)
	require.NoError(t, err)
	for _, ent := range ents {
		os.Remove(filepath.Join(tb.chunkdir, ent.Name()))
	}

	// should succeed anyway
	mp1 := tb.materialize("qa22bifihaxyvn6q2a6w9m0nklqrk9wh-opusfile-0.12")
	require.Equal(t, "1rswindywkyq2jmfpxd6n772jii3z5xz6ypfbb63c17k5il39hfm", tb.nixHash(mp1))
}
