package devmapper

import (
	"fmt"
	"io/fs"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SectorSize is a device size used for devmapper calculations. Currently this value hardcoded to 512.
const SectorSize = 512

const (
	// ReadOnlyFlag is a devmapper readonly flag value
	ReadOnlyFlag = unix.DM_READONLY_FLAG
)

// Table is a type to represent different devmapper targets like 'zero', 'crypt', ...
type Table interface {
	start() uint64
	length() uint64
	targetType() string
	buildSpec() string // see https://wiki.gentoo.org/wiki/Device-mapper for examples of specs
	openVolume(flag int, perm fs.FileMode) (Volume, error)
}

var errNotImplemented = fmt.Errorf("not implemented")

// Create creates a new device. No table will be loaded. The device will be in
// suspended state. Any IO to this device will fail.
func Create(name string, uuid string) error {
	return ioctlTable(unix.DM_DEV_CREATE, name, uuid, 0, false, nil)
}

// CreateAndLoad creates, loads the provided tables and resumes the device.
func CreateAndLoad(name string, uuid string, flags uint32, tables ...Table) error {
	if err := Create(name, uuid); err != nil {
		return err
	}
	if err := Load(name, flags, tables...); err != nil {
		_ = Remove(name)
		return err
	}
	return Resume(name)
}

// Message passes a message string to the target at specific offset of a device.
func Message(name string, sector int, message string) error {
	return errNotImplemented
}

// Suspend suspends the given device.
func Suspend(name string) error {
	return ioctlTable(unix.DM_DEV_SUSPEND, name, "", unix.DM_SUSPEND_FLAG, false, nil)
}

// Resume resumes the given device.
func Resume(name string) error {
	return ioctlTable(unix.DM_DEV_SUSPEND, name, "", 0, true, nil)
}

// Load loads given table into the device
func Load(name string, flags uint32, tables ...Table) error {
	flags &= unix.DM_READONLY_FLAG
	return ioctlTable(unix.DM_TABLE_LOAD, name, "", flags, false, tables)
}

// Rename renames the device
func Rename(old, new string) error {
	// primaryUdevEvent == true
	return errNotImplemented
}

// SetUUID sets uuid for a given device
func SetUUID(name, uuid string) error {
	return errNotImplemented
}

// Remove removes the device and destroys its tables.
func Remove(name string) error {
	return ioctlTable(unix.DM_DEV_REMOVE, name, "", 0, true, nil)
}

// ListItem represents information about a dmsetup device
type ListItem struct {
	DevNo uint64
	Name  string
}

// List provides a list of dmsetup devices
func List() ([]ListItem, error) {
	bufferSize := 4096

retry:
	data := make([]byte, bufferSize) // unix.SizeofDmIoctl info + space for output params
	ioctlData := (*unix.DmIoctl)(unsafe.Pointer(&data[0]))
	ioctlData.Version = [...]uint32{4, 0, 0} // minimum required version
	ioctlData.Data_size = uint32(bufferSize)
	ioctlData.Data_start = unix.SizeofDmIoctl

	if err := ioctl(unix.DM_LIST_DEVICES, data); err != nil {
		return nil, err
	}

	if ioctlData.Flags&unix.DM_BUFFER_FULL_FLAG != 0 {
		if bufferSize >= 1024*1024 { // 1 MB
			return nil, fmt.Errorf("ioctl(DM_LIST_DEVICES): output data is too big")
		}
		bufferSize *= 4
		goto retry // retry with bigger buffer
	}

	result := make([]ListItem, 0)

	type dmNameList struct { // reflects struct dm_name_list
		devNo      uint64
		offsetNext uint32
	}
	offset := ioctlData.Data_start
	for offset < ioctlData.Data_size {
		item := (*dmNameList)(unsafe.Pointer(&data[offset]))
		nameData := data[offset+12:] // 12 is sum of the dmNameList fields, at this offset "name" field starts
		if item.offsetNext != 0 {
			nameData = nameData[:item.offsetNext] // make sure that nameData never captures data from the next item
		}
		dev := ListItem{
			DevNo: item.devNo,
			Name:  fixedArrayToString(nameData),
		}
		result = append(result, dev)

		if item.offsetNext == 0 {
			break
		}
		offset += item.offsetNext
	}

	return result, nil
}

// DeviceInfo is a type that holds devmapper device information
type DeviceInfo struct {
	Name       string
	UUID       string
	DevNo      uint64
	OpenCount  int32
	TargetsNum uint32
	Flags      uint32 // combination of unix.DM_*_FLAG
}

// InfoByName returns device information by its name
func InfoByName(name string) (*DeviceInfo, error) {
	return info(name, 0)
}

// InfoByDevno returns device mapper information by its block device number (major/minor)
func InfoByDevno(devno uint64) (*DeviceInfo, error) {
	return info("", devno)
}

func info(name string, devno uint64) (*DeviceInfo, error) {
	data := make([]byte, unix.SizeofDmIoctl)
	ioctlData := (*unix.DmIoctl)(unsafe.Pointer(&data[0]))
	ioctlData.Version = [...]uint32{4, 0, 0} // minimum required version
	copy(ioctlData.Name[:], name)
	ioctlData.Dev = devno
	ioctlData.Data_size = unix.SizeofDmIoctl
	ioctlData.Data_start = unix.SizeofDmIoctl

	if err := ioctl(unix.DM_DEV_STATUS, data); err != nil {
		return nil, err
	}

	const flagsVisibleToUser = unix.DM_READONLY_FLAG | unix.DM_SUSPEND_FLAG | unix.DM_ACTIVE_PRESENT_FLAG | unix.DM_INACTIVE_PRESENT_FLAG | unix.DM_DEFERRED_REMOVE | unix.DM_INTERNAL_SUSPEND_FLAG
	info := DeviceInfo{
		Name:       fixedArrayToString(ioctlData.Name[:]),
		UUID:       fixedArrayToString(ioctlData.Uuid[:]),
		DevNo:      ioctlData.Dev,
		OpenCount:  ioctlData.Open_count,
		TargetsNum: ioctlData.Target_count,
		Flags:      ioctlData.Flags & flagsVisibleToUser,
	}
	return &info, nil
}

// GetVersion returns version for the dm-mapper kernel interface
func GetVersion() (major, minor, patch uint32, err error) {
	data := make([]byte, unix.SizeofDmIoctl)
	ioctlData := (*unix.DmIoctl)(unsafe.Pointer(&data[0]))
	ioctlData.Version = [...]uint32{4, 0, 0} // minimum required version
	ioctlData.Data_size = unix.SizeofDmIoctl
	ioctlData.Data_start = unix.SizeofDmIoctl

	if err := ioctl(unix.DM_VERSION, data); err != nil {
		return 0, 0, 0, err
	}

	return ioctlData.Version[0], ioctlData.Version[1], ioctlData.Version[2], nil
}
