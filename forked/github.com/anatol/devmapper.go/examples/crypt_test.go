package test

import (
	"crypto/rand"
	"os"
	"testing"

	"github.com/anatol/devmapper.go"
	"github.com/freddierice/go-losetup/v2"
	"github.com/stretchr/testify/require"
)

func TestCryptTarget(t *testing.T) {
	name := "test.crypttarget"
	uuid := "2f144136-b0de-4b51-b2eb-bd869cc39a6e"

	dir := t.TempDir()
	backingFile := dir + "/backing"
	f, err := os.Create(backingFile)
	require.NoError(t, err)
	defer f.Close()

	size := uint64(40) * devmapper.SectorSize
	require.NoError(t, f.Truncate(int64(size)))

	loop, err := losetup.Attach(backingFile, 0, false)
	require.NoError(t, err)
	defer loop.Detach()

	key := make([]byte, 32)
	c := devmapper.CryptTable{
		Length:        size,
		Encryption:    "aes-xts-plain64",
		Key:           key,
		BackendDevice: loop.Path(),
		Flags: []string{
			devmapper.CryptFlagAllowDiscards,
			devmapper.CryptFlagNoReadWorkqueue,
			devmapper.CryptFlagNoWriteWorkqueue,
			devmapper.CryptFlagSameCPUCrypt,
			devmapper.CryptFlagSubmitFromCryptCPUs,
		},
	}
	require.NoError(t, devmapper.CreateAndLoad(name, uuid, 0, c))
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
}

func TestCryptTargetUserspaceRead(t *testing.T) {
	name := "test.crypttarget.userspaceread"
	uuid := "2f144136-b0de-4b51-b2ea-bd869cc39a6e"

	dir := t.TempDir()
	backingFile := dir + "/backing"
	f, err := os.Create(backingFile)
	require.NoError(t, err)
	size := uint64(40) * devmapper.SectorSize
	require.NoError(t, f.Truncate(int64(size)+1024))
	require.NoError(t, f.Close())

	loop, err := losetup.Attach(backingFile, 0, false)
	require.NoError(t, err)
	defer loop.Detach()

	key := make([]byte, 32)
	rand.Read(key)
	c := devmapper.CryptTable{
		Length:        size,
		Encryption:    "aes-xts-plain64",
		Key:           key,
		BackendDevice: loop.Path(),
		BackendOffset: 1024,
	}
	require.NoError(t, devmapper.CreateAndLoad(name, uuid, 0, c))
	defer devmapper.Remove(name)

	fname := "/dev/mapper/" + name
	require.NoError(t, waitForFile(fname))

	expected := make([]byte, 5*devmapper.SectorSize)
	rand.Read(expected)
	copy(expected, "Hello, world!")
	require.NoError(t, os.WriteFile(fname, expected, 0))

	c.BackendDevice = backingFile
	v, err := devmapper.OpenUserspaceVolume(os.O_RDONLY, 0, c)
	require.NoError(t, err)
	defer v.Close()

	buf := make([]byte, 5*devmapper.SectorSize)
	_, err = v.ReadAt(buf, 0)
	require.NoError(t, err)
	require.Equal(t, expected, buf)
}

func TestCryptTargetUserspaceWrite(t *testing.T) {
	name := "test.crypttarget.userspacewrite"
	uuid := "2f144136-b0de-4b51-b2ea-bd869ce39a6e"

	dir := t.TempDir()
	backingFile := dir + "/backing"
	f, err := os.Create(backingFile)
	require.NoError(t, err)
	size := uint64(40) * devmapper.SectorSize
	require.NoError(t, f.Truncate(int64(size)+1024))
	require.NoError(t, f.Close())

	key := make([]byte, 32)
	rand.Read(key)
	c := devmapper.CryptTable{
		Length:        size,
		Encryption:    "aes-xts-plain64",
		Key:           key,
		BackendDevice: backingFile,
	}

	v, err := devmapper.OpenUserspaceVolume(os.O_RDWR, 0, c)
	require.NoError(t, err)
	defer v.Close()

	expected := make([]byte, 5*devmapper.SectorSize)
	rand.Read(expected)
	copy(expected, "Hello, world!")
	_, err = v.WriteAt(expected, 512)
	require.NoError(t, err)
	bufRead := make([]byte, 5*devmapper.SectorSize)
	_, err = v.ReadAt(bufRead, 512)
	require.NoError(t, err)
	require.Equal(t, expected, bufRead)
	v.Close()

	loop, err := losetup.Attach(backingFile, 0, true)
	require.NoError(t, err)
	defer loop.Detach()

	c.BackendDevice = loop.Path()
	require.NoError(t, devmapper.CreateAndLoad(name, uuid, 0, c))
	defer devmapper.Remove(name)

	fname := "/dev/mapper/" + name
	require.NoError(t, waitForFile(fname))

	buf, err := os.ReadFile(fname)
	require.NoError(t, err)
	buf = buf[512:][:5*devmapper.SectorSize]
	require.Equal(t, expected, buf)
}

func TestCryptTargetUserspaceReadCustomSectorSize(t *testing.T) {
	name := "test.crypttarget.userspaceread"
	uuid := "2f144136-b0de-4b51-b2ea-bd869cc39a6e"

	dir := t.TempDir()
	backingFile := dir + "/backing"
	f, err := os.Create(backingFile)
	require.NoError(t, err)
	size := uint64(40) * devmapper.SectorSize
	require.NoError(t, f.Truncate(int64(size)+4096))
	require.NoError(t, f.Close())

	loop, err := losetup.Attach(backingFile, 0, false)
	require.NoError(t, err)
	defer loop.Detach()

	sectorSize := uint64(2048)

	key := make([]byte, 32)
	rand.Read(key)
	c := devmapper.CryptTable{
		Length:        size,
		Encryption:    "aes-xts-plain64",
		Key:           key,
		BackendDevice: loop.Path(),
		BackendOffset: 4096,
		SectorSize:    sectorSize,
	}
	require.NoError(t, devmapper.CreateAndLoad(name, uuid, 0, c))
	defer devmapper.Remove(name)

	fname := "/dev/mapper/" + name
	require.NoError(t, waitForFile(fname))

	expected := make([]byte, 3*sectorSize)
	rand.Read(expected)
	copy(expected, "Hello, world!")
	require.NoError(t, os.WriteFile(fname, expected, 0))
	devmapper.Remove(name)
	loop.Detach()

	c.BackendDevice = backingFile
	v, err := devmapper.OpenUserspaceVolume(os.O_RDONLY, 0, c)
	require.NoError(t, err)
	defer v.Close()

	buf := make([]byte, 3*sectorSize)
	_, err = v.ReadAt(buf, 0)
	require.NoError(t, err)
	require.Equal(t, expected, buf)
}
