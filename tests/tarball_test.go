package tests

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/dnr/styx/common/client"
	"github.com/dnr/styx/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTarball(t *testing.T) {
	tb := newTestBase(t)
	tb.startAll()

	tbRes := tb.tarball(tb.upstreamUrl + "tarballs/nix-1.0.tar.gz")
	assert.Equal(t, "nix-1.0", tbRes.StorePathName)
	assert.Equal(t, "49i4rkskr9m6y1inclhbrvvb3fv39q91", tbRes.StorePathHash)
	assert.Equal(t, "b274771c9a0e4ed2f99de20ac3152654dba12183de2326729d02546dd0d50095", tbRes.NarHash)

	// materialize manually since we need a special "upstream"
	mp := filepath.Join(t.TempDir(), "mp")
	c := client.NewClient(filepath.Join(tb.cachedir, "styx.sock"))
	var res daemon.Status
	code, err := c.Call(daemon.MaterializePath, daemon.MaterializeReq{
		Upstream:  "http://localhost:7444", // daemon.fakeCacheBind
		StorePath: tbRes.StorePathHash + "-" + tbRes.StorePathName,
		DestPath:  mp,
	}, &res)
	require.NoError(t, err)
	require.Equal(t, code, http.StatusOK)
	require.True(t, res.Success, "error:", res.Error)
	// note same as nar hash above, in base32
	assert.Equal(t, "1580sp86sm02kmr2c8yyhchs3nsl4qaw62p2kpwx4khfk8f7fx5j", tb.nixHash(mp))
}
