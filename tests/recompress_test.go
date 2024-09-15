package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecompress(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	mp1 := tb.mount("cd1nbildgzzfryjg82njnn36i4ynyf8h-bash-interactive-5.1-p16-man")
	man1 := filepath.Join(mp1, "share", "man", "man1", "bash.1.gz")
	require.Equal(t, "0s9d681f8smlsdvbp6lin9qrbsp3hz3dnf4pdhwi883v8l1486r7", tb.nixHash(man1))
	d1 := tb.debug()
	require.NotZero(t, d1.Stats.BatchReqs)
	require.Zero(t, d1.Stats.SingleReqs+d1.Stats.DiffReqs)

	mp2 := tb.mount("8vyj9c6g424mz0v3kvzkskhvzhwj6288-bash-interactive-5.2-p15-man")
	man2 := filepath.Join(mp2, "share", "man", "man1", "bash.1.gz")
	require.Equal(t, "0r2agiq8bzv09nsk11yidwvyjb5cfrp5wavq2jxqc9k6sh1256s9", tb.nixHash(man2))
	d2 := tb.debug()
	require.NotZero(t, d2.Stats.DiffReqs)
	require.Greater(t, d2.Stats.DiffBytes, int64(0))
	// the zstd diff between the uncompressed files is < 7kb. between compressed files is 95kb
	// (no savings). check that we did the recompress thing.
	require.Less(t, d2.Stats.DiffBytes, int64(10000))

	require.Zero(t, d2.Stats.SingleErrs+d2.Stats.BatchErrs+d2.Stats.DiffErrs)
}
