package test

import (
	"os"
	"testing"

	"github.com/anatol/devmapper.go"
	"github.com/freddierice/go-losetup/v2"
	"github.com/stretchr/testify/require"
)

func TestLinear(t *testing.T) {
	name := "test.lineartarget"
	uuid := "b7d95b61-e077-4452-b904-13bd6936b44d"

	dir := t.TempDir()
	backingFile := dir + "/backing"
	f, err := os.Create(backingFile)
	require.NoError(t, err)
	defer f.Close()

	text := "Hello, world!!!"
	_, err = f.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(10*1024))
	require.NoError(t, f.Close())

	loop, err := losetup.Attach(backingFile, 0, false)
	require.NoError(t, err)
	defer loop.Detach()

	l := devmapper.LinearTable{
		Length:        15 * devmapper.SectorSize,
		BackendDevice: loop.Path(),
	}
	require.NoError(t, devmapper.CreateAndLoad(name, uuid, 0, l))
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
	require.Equal(t, 15*devmapper.SectorSize, len(data), "invalid file length")
	expectedData := make([]byte, 15*devmapper.SectorSize)
	copy(expectedData, text)
	require.Equal(t, expectedData, data, "data read from the mapper differs from the backing file")
}
