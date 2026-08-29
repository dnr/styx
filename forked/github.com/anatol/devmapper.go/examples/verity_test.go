package test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/anatol/devmapper.go"
	"github.com/freddierice/go-losetup/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestVerity(t *testing.T) {
	dir := t.TempDir()

	// Setup data device
	d, err := os.Create(dir + "/data")
	require.NoError(t, err)
	defer d.Close()
	// Write some data there
	_, err = d.WriteString("Hello verity!!!!")
	require.NoError(t, err)

	dataSize := uint64(4096) * devmapper.SectorSize
	require.NoError(t, d.Truncate(int64(dataSize)))
	dLoop, err := losetup.Attach(dir+"/data", 0, false)
	require.NoError(t, err)
	defer dLoop.Detach()

	// Setup hash device
	h, err := os.Create(dir + "/hash")
	require.NoError(t, err)
	defer h.Close()
	require.NoError(t, h.Truncate(100*devmapper.SectorSize))
	hLoop, err := losetup.Attach(dir+"/hash", 0, false)
	require.NoError(t, err)
	defer hLoop.Detach()

	// format hash device
	salt, err := randomHex(32)
	require.NoError(t, err)
	out, err := exec.Command("veritysetup", "format", "--no-superblock", "--salt", salt, dLoop.Path(), hLoop.Path()).Output()
	require.NoError(t, err)
	props, err := parseProperties(out)
	require.NoError(t, err)

	v := devmapper.VerityTable{
		Length:        dataSize,
		HashType:      1, // the same as props["Hash type"]
		DataDevice:    dLoop.Path(),
		HashDevice:    hLoop.Path(),
		DataBlockSize: 4096,     // 4096 is the default value, the same as props["Data block size"]
		HashBlockSize: 4096,     // 4096 is the default value, the same as props["Hash block size"]
		NumDataBlocks: 512,      // size of the device in "DataBlockSize" blocks
		Algorithm:     "sha256", // the same as props["Hash algorithm"]
		Salt:          salt,
		Digest:        props["Root hash"],
	}
	name := "test.verity"
	uuid := "79639465-407e-4af6-b76b-d98a70c0d5d3"
	require.NoError(t, devmapper.CreateAndLoad(name, uuid, devmapper.ReadOnlyFlag, v))
	defer devmapper.Remove(name)

	got, err := devInfo(name)
	require.NoError(t, err)
	checkDevInfo(t, got, map[string]string{
		PropName:          name,
		PropTargetsNum:    "1",
		PropState:         "ACTIVE (READ-ONLY)",
		PropTablesPresent: "LIVE",
		PropUUID:          uuid,
	})

	mapper := "/dev/mapper/" + name
	require.NoError(t, waitForFile(mapper))

	// Now verify the data read from the mapper
	data, err := os.ReadFile(mapper)
	require.NoError(t, err)
	expectedData := make([]byte, 4096*devmapper.SectorSize)
	copy(expectedData, "Hello verity!!!!")
	require.Equal(t, expectedData, data, "data read from the mapper differs from the backing file")

	// Now corrupt the backing file (flip the first character from H to h)
	// verity should fail
	_, err = d.WriteAt([]byte{'h'}, 0)
	require.NoError(t, err)
	_, err = os.ReadFile(mapper)
	require.NotNil(t, "expected EIO if backing device is corrupted")
	require.ErrorIs(t, err, unix.EIO, "unexpected error on verity corruption")
}
