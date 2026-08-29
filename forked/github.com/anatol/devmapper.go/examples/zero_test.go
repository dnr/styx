package test

import (
	"os"
	"testing"

	"github.com/anatol/devmapper.go"
	"github.com/stretchr/testify/require"
)

func TestZeroTarget(t *testing.T) {
	name := "test.zerotarget"
	uuid := "2fa44836-b0de-4b51-b2eb-bd811cc39a6e"
	z := devmapper.ZeroTable{Length: 200 * devmapper.SectorSize}
	require.NoError(t, devmapper.CreateAndLoad(name, uuid, 0, z))
	defer devmapper.Remove(name)

	got, err := devInfo(name)
	require.NoError(t, err)
	checkDevInfo(t, got, map[string]string{
		PropName:          name,
		PropTargetsNum:    "1",
		PropState:         "ACTIVE",
		PropTablesPresent: "LIVE",
		PropUUID:          uuid,
	})

	mapper := "/dev/mapper/" + name
	require.NoError(t, waitForFile(mapper))

	data, err := os.ReadFile(mapper)
	require.NoError(t, err)

	zeros := make([]byte, 200*devmapper.SectorSize)
	require.Equal(t, zeros, data)
}

func TestUserspaceZeroTarget(t *testing.T) {
	z := devmapper.ZeroTable{Length: 200 * devmapper.SectorSize}
	v, err := devmapper.OpenUserspaceVolume(os.O_RDONLY, 0, z)
	require.NoError(t, err)

	buf := make([]byte, 512)
	_, err = v.ReadAt(buf, 34)
	require.Error(t, err)
	_, err = v.ReadAt(buf, 1024)
	require.NoError(t, err)

	zeroBuf := make([]byte, 512)
	require.Equal(t, zeroBuf, buf)
}
