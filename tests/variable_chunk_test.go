package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dnr/styx/common/shift"
)

func TestVariableChunk(t *testing.T) {
	tb := newTestBase(t)
	// override this to force different chunk sizes
	tb.chunkSizer = func(size int64) shift.Shift {
		switch {
		case size <= 25<<12: // 100 KiB
			return 12
		case size <= 50<<13: // 400 KiB
			return 13
		case size <= 50<<17: // 6.4 MiB
			return 17
		default:
			return 20
		}
	}
	tb.startAll()

	// 12 files in the 400-6.4mb bucket, 10 in 100-400, rest < 100
	mp1 := tb.mount("kbi7qf642gsxiv51yqank8bnx39w3crd-calf-0.90.3")
	require.Equal(t, "1bhyfn2k8w41cx7ddarmjmwscas0946n6gw5mralx9lg0vbbcx6d", tb.nixHash(mp1))

	// 1 file > 6.4mb
	mp2 := tb.mount("d30xd6x3669hg2a6xwjb1r3nb9a99sw2-openblas-0.3.27")
	require.Equal(t, "158wdip28fcfvw911s1wx8h132ljzvzqa3m5zgjj4vn0hby9kzzn", tb.nixHash(mp2))
}
