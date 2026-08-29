package test

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"

	"github.com/anatol/devmapper.go"
	"github.com/freddierice/go-losetup/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestCreate(t *testing.T) {
	name2 := "test.create2"
	uuid2 := "634d7039-f339-4bbb-860a-e8a8c865a722"
	require.NoError(t, devmapper.Create(name2, uuid2))
	defer devmapper.Remove(name2)

	name := "test.create"
	uuid := "634d7039-f339-4bbb-860a-e8a8c865a729"
	require.NoError(t, devmapper.Create(name, uuid))
	defer devmapper.Remove(name)

	// check the target exists
	got, err := devInfo(name)
	require.NoError(t, err)
	checkDevInfo(t, got, map[string]string{
		PropName:          name,
		PropState:         "ACTIVE",
		PropTablesPresent: "None",
		PropUUID:          uuid,
	})

	list, err := devmapper.List()
	require.NoError(t, err)
	// this test can be run as root at a developer machine, and it can be more mapper devices not related to this test
	require.LessOrEqual(t, 2, len(list), "expected 2 mapper devices")
	found := false
	for _, l := range list {
		if l.Name == name {
			expectedDevNo := got["Major, minor"]
			gotDevNo := fmt.Sprintf("%d, %d", unix.Major(l.DevNo), unix.Minor(l.DevNo))
			require.Equal(t, expectedDevNo, gotDevNo)
			found = true
			break
		}
	}
	require.Truef(t, found, "devmapper.List() did not find a device with name %s", name)

	info, err := devmapper.InfoByName(name)
	require.NoError(t, err)
	checkDevInfo(t, got, map[string]string{
		PropName:       info.Name,
		PropUUID:       info.UUID,
		PropDevId:      fmt.Sprintf("%d, %d", unix.Major(info.DevNo), unix.Minor(info.DevNo)),
		PropOpenCount:  strconv.Itoa(int(info.OpenCount)),
		PropTargetsNum: strconv.Itoa(int(info.TargetsNum)),
		PropState:      "ACTIVE",
	})

	infoByDev, err := devmapper.InfoByDevno(info.DevNo)
	require.NoError(t, err)
	require.Equal(t, name, infoByDev.Name)

	require.NoError(t, devmapper.Remove(name))

	// check the target got removed
	_, err = devInfo(name)

	require.NotNil(t, err)
	require.Contains(t, err.Error(), "Device does not exist.", "dm device %s should be removed", name)
}

// create 3 files with 5 sectors each (3*512 bytes) then join it
func TestJoinedDevices(t *testing.T) {
	name := "test.joinedtarget"
	uuid := "2c4c1a46-0c00-4430-b708-bdd16d34e7bf"

	dir := t.TempDir()

	targets := make([]devmapper.Table, 0, 3)

	for i := 0; i < 3; i++ {
		backingFile := dir + "/backing." + strconv.Itoa(i)
		f, err := os.Create(backingFile)
		require.NoError(t, err)
		defer f.Close()

		text := fmt.Sprintf("Hello, world %d !!!", i)
		_, err = f.WriteString(text)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(5*devmapper.SectorSize))

		loop, err := losetup.Attach(backingFile, 0, false)
		require.NoError(t, err)
		defer loop.Detach()

		l := devmapper.LinearTable{
			Start:         5 * uint64(i) * devmapper.SectorSize,
			Length:        5 * devmapper.SectorSize,
			BackendDevice: loop.Path(),
		}

		targets = append(targets, l)
	}

	require.NoError(t, devmapper.CreateAndLoad(name, uuid, 0, targets...))
	defer devmapper.Remove(name)

	got, err := devInfo(name)
	require.NoError(t, err)
	checkDevInfo(t, got, map[string]string{
		PropName:          name,
		PropTargetsNum:    "3",
		PropState:         "ACTIVE",
		PropTablesPresent: "LIVE",
		PropUUID:          uuid,
	})

	mapperFile := "/dev/mapper/" + name
	require.NoError(t, waitForFile(mapperFile))
	data, err := os.ReadFile(mapperFile)
	require.NoError(t, err)
	require.Equal(t, 15*devmapper.SectorSize, len(data))
	expectedData := make([]byte, 15*devmapper.SectorSize)
	copy(expectedData[0*devmapper.SectorSize:], "Hello, world 0 !!!")  // beginning of the 1st device
	copy(expectedData[5*devmapper.SectorSize:], "Hello, world 1 !!!")  // beginning of the 2nd device
	copy(expectedData[10*devmapper.SectorSize:], "Hello, world 2 !!!") // beginning of the 3rd device

	require.Equal(t, expectedData, data, "data read from the mapper differs from the backing file")
}

// split backing device into 2 devmap devices
func TestSplitDevices(t *testing.T) {
	dir := t.TempDir()

	backingFile := dir + "/backing"
	f, err := os.Create(backingFile)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, f.Truncate(50*devmapper.SectorSize))
	_, err = f.WriteAt([]byte("Hello, world 0 !!!"), 0)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte("Hello, world 1 !!!"), 5*devmapper.SectorSize+100)
	require.NoError(t, err)

	loop, err := losetup.Attach(backingFile, 0, false)
	require.NoError(t, err)
	defer loop.Detach()

	name1 := "test.splittarget1"
	uuid1 := "8fb63cb9-e92c-473e-bad2-3dffd17859e7"
	l1 := devmapper.LinearTable{
		Length:        5 * devmapper.SectorSize,
		BackendDevice: loop.Path(),
		BackendOffset: 0,
	}
	require.NoError(t, devmapper.CreateAndLoad(name1, uuid1, 0, l1))
	defer devmapper.Remove(name1)

	name2 := "test.splittarget2"
	uuid2 := "790dd808-3b13-46e1-9d4a-42053580010c"
	l2 := devmapper.LinearTable{
		Length:        45 * devmapper.SectorSize,
		BackendDevice: loop.Path(),
		BackendOffset: 5 * devmapper.SectorSize,
	}
	require.NoError(t, devmapper.CreateAndLoad(name2, uuid2, 0, l2))
	defer devmapper.Remove(name2)

	got1, err := devInfo(name1)
	require.NoError(t, err)
	checkDevInfo(t, got1, map[string]string{
		PropName:          name1,
		PropTargetsNum:    "1",
		PropState:         "ACTIVE",
		PropTablesPresent: "LIVE",
		PropUUID:          uuid1,
	})

	got2, err := devInfo(name2)
	require.NoError(t, err)
	checkDevInfo(t, got2, map[string]string{
		PropName:          name2,
		PropTargetsNum:    "1",
		PropState:         "ACTIVE",
		PropTablesPresent: "LIVE",
		PropUUID:          uuid2,
	})

	mapper1 := "/dev/mapper/" + name1
	require.NoError(t, waitForFile(mapper1))

	data1, err := os.ReadFile(mapper1)
	require.NoError(t, err)
	require.Equal(t, 5*devmapper.SectorSize, len(data1))
	expectedData := make([]byte, 5*devmapper.SectorSize)
	copy(expectedData, "Hello, world 0 !!!")
	require.Equal(t, expectedData, data1, "data read from the mapper differs from the backing file")

	mapper2 := "/dev/mapper/" + name2
	require.NoError(t, waitForFile(mapper2))

	data2, err := os.ReadFile(mapper2)
	require.NoError(t, err)
	require.Equal(t, 45*devmapper.SectorSize, len(data2), "invalid file length")
	expectedData2 := make([]byte, 45*devmapper.SectorSize)
	copy(expectedData2[100:], "Hello, world 1 !!!")
	require.Equal(t, expectedData2, data2, "data read from the mapper differs from the backing file")
}

func TestGetVersion(t *testing.T) {
	output, err := exec.Command("dmsetup", "version").Output()
	require.NoError(t, err)
	re := regexp.MustCompile(`Driver version:\W+(\d+)\.(\d+)\.(\d+)`)
	matches := re.FindAllStringSubmatch(string(output), -1)
	major, err := strconv.Atoi(matches[0][1])
	require.NoError(t, err)
	minor, err := strconv.Atoi(matches[0][2])
	require.NoError(t, err)
	patch, err := strconv.Atoi(matches[0][3])
	require.NoError(t, err)

	gotMajor, gotMinor, gotPatch, err := devmapper.GetVersion()
	require.NoError(t, err)

	require.Equal(t, []uint32{uint32(major), uint32(minor), uint32(patch)}, []uint32{gotMajor, gotMinor, gotPatch}, "device mapper version mismatch")
}

// create 3 files with 5 sectors each (3*512 bytes) then join it
func TestUserspaceJoinedDevicesRead(t *testing.T) {
	dir := t.TempDir()

	targets := make([]devmapper.Table, 0, 3)

	for i := 0; i < 3; i++ {
		backingFile := dir + "/backing." + strconv.Itoa(i)
		f, err := os.Create(backingFile)
		require.NoError(t, err)
		defer f.Close()

		text := fmt.Sprintf("Hello, world %d !!!", i)
		_, err = f.WriteString(text)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(5*devmapper.SectorSize))

		l := devmapper.LinearTable{
			Start:         5 * uint64(i) * devmapper.SectorSize,
			Length:        5 * devmapper.SectorSize,
			BackendDevice: backingFile,
		}

		targets = append(targets, l)
	}

	v, err := devmapper.OpenUserspaceVolume(os.O_RDONLY, 0, targets...)
	require.NoError(t, err)
	defer v.Close()

	buf := make([]byte, 15*devmapper.SectorSize)
	n, err := v.ReadAt(buf, 0)
	require.NoError(t, err)
	require.Equal(t, 15*devmapper.SectorSize, n)

	require.NoError(t, err)
	expectedData := make([]byte, 15*devmapper.SectorSize)
	copy(expectedData[0*devmapper.SectorSize:], "Hello, world 0 !!!")  // beginning of the 1st device
	copy(expectedData[5*devmapper.SectorSize:], "Hello, world 1 !!!")  // beginning of the 2nd device
	copy(expectedData[10*devmapper.SectorSize:], "Hello, world 2 !!!") // beginning of the 3rd device

	require.Equal(t, expectedData, buf, "data read from the volume differs from the backing file")
}

// create 3 files with 5 sectors each (3*512 bytes) then join it
func TestUserspaceJoinedDevicesWrite(t *testing.T) {
	dir := t.TempDir()

	targets := make([]devmapper.Table, 0, 3)

	for i := 0; i < 3; i++ {
		backingFile := dir + "/backing." + strconv.Itoa(i)
		f, err := os.Create(backingFile)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(5*devmapper.SectorSize))
		f.Close()

		l := devmapper.LinearTable{
			Start:         5 * uint64(i) * devmapper.SectorSize,
			Length:        5 * devmapper.SectorSize,
			BackendDevice: backingFile,
		}

		targets = append(targets, l)
	}

	v, err := devmapper.OpenUserspaceVolume(os.O_RDWR, 0, targets...)
	require.NoError(t, err)
	defer v.Close()

	for i := 0; i < 3; i++ {
		buf := make([]byte, devmapper.SectorSize)
		text := fmt.Sprintf("Hello, world %d !!!", i)
		copy(buf, text)
		n, err := v.WriteAt(buf, int64(5*i*devmapper.SectorSize))
		require.NoError(t, err)
		require.Equal(t, devmapper.SectorSize, n)
	}

	buf := make([]byte, 15*devmapper.SectorSize)
	n, err := v.ReadAt(buf, 0)
	require.NoError(t, err)
	require.Equal(t, 15*devmapper.SectorSize, n)

	expectedData := make([]byte, 15*devmapper.SectorSize)
	copy(expectedData[0*devmapper.SectorSize:], "Hello, world 0 !!!")  // beginning of the 1st device
	copy(expectedData[5*devmapper.SectorSize:], "Hello, world 1 !!!")  // beginning of the 2nd device
	copy(expectedData[10*devmapper.SectorSize:], "Hello, world 2 !!!") // beginning of the 3rd device
	require.Equal(t, expectedData, buf, "data read from the volume differs from the backing file")

	for i := 0; i < 3; i++ {
		backingFile := dir + "/backing." + strconv.Itoa(i)
		buf, err := os.ReadFile(backingFile)
		require.NoError(t, err)

		expectedData := make([]byte, 5*devmapper.SectorSize)
		text := fmt.Sprintf("Hello, world %d !!!", i)
		copy(expectedData, text)

		require.Equal(t, expectedData, buf)
	}
}
