package devmapper

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioctlTable executes a device mapper ioctl with a set of table specs passed as a payload.
// udevEvent is a boolean field that sets DM_UDEV_PRIMARY_SOURCE_FLAG udev flag.
// This flag is later processed by rules at /usr/lib/udev/rules.d/10-dm.rules
// Per devicecrypt sourcecode only RESUME, REMOVE, RENAME operations need to have DM_UDEV_PRIMARY_SOURCE_FLAG
// flag set.
func ioctlTable(cmd uintptr, name string, uuid string, flags uint32, primaryUdevEvent bool, tables []Table) error {
	const (
		// allocate buffer large enough for dmioctl + specs
		alignment = 8

		DM_UDEV_FLAGS_SHIFT = 16
		// Quoting https://fossies.org/linux/LVM2/libdm/libdevmapper.h
		//
		// DM_UDEV_PRIMARY_SOURCE_FLAG is automatically appended by
		// libdevmapper for all ioctls generating udev uevents. Once used in
		// udev rules, we know if this is a real "primary sourced" event or not.
		// We need to distinguish real events originated in libdevmapper from
		// any spurious events to gather all missing information (e.g. events
		// generated as a result of "udevadm trigger" command or as a result
		// of the "watch" udev rule).
		DM_UDEV_PRIMARY_SOURCE_FLAG = 0x0040
	)

	var udevFlags uint32
	if primaryUdevEvent {
		// device mapper has a complex initialization sequence. A device need to be 1) created
		// 2) load table 3) resumed. The device is usable at the 3rd step only.
		// To make udev rules handle the device at 3rd step (rather than at ADD event), device mapper distinguishes
		// the "primary" events with a udev flag set below.
		// Only RESUME, REMOVE, RENAME operations are considered primary events.
		udevFlags = DM_UDEV_PRIMARY_SOURCE_FLAG << DM_UDEV_FLAGS_SHIFT
	}

	specs := make([]string, 0, len(tables)) // cached specs

	length := unix.SizeofDmIoctl
	for _, t := range tables {
		length += unix.SizeofDmTargetSpec
		spec := t.buildSpec()
		specs = append(specs, spec)

		length += roundUp(len(spec)+1, alignment) // adding 1 for terminating NUL, then align the data
	}

	data := make([]byte, length)
	var idx uintptr
	ioctlData := (*unix.DmIoctl)(unsafe.Pointer(&data[0]))
	ioctlData.Version = [...]uint32{4, 0, 0} // minimum required version
	copy(ioctlData.Name[:], name)
	copy(ioctlData.Uuid[:], uuid)
	ioctlData.Data_size = uint32(length)
	ioctlData.Data_start = unix.SizeofDmIoctl
	ioctlData.Target_count = uint32(len(tables))
	ioctlData.Flags = flags
	ioctlData.Event_nr = udevFlags
	idx += unix.SizeofDmIoctl

	for i, t := range tables {
		spec := specs[i]

		specData := (*unix.DmTargetSpec)(unsafe.Pointer(&data[idx]))
		specSize := unix.SizeofDmTargetSpec + uintptr(roundUp(len(spec)+1, alignment))
		specData.Next = uint32(specSize)
		specData.Sector_start = t.start() / SectorSize
		specData.Length = t.length() / SectorSize
		copy(specData.Target_type[:], t.targetType())
		copy(data[idx+unix.SizeofDmTargetSpec:], spec)

		idx += specSize
	}

	return ioctl(cmd, data)
}

func ioctl(cmd uintptr, data []byte) error {
	controlFile, err := os.Open("/dev/mapper/control")
	if err != nil {
		return err
	}
	defer controlFile.Close()

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		controlFile.Fd(),
		cmd,
		uintptr(unsafe.Pointer(&data[0])),
	)
	if errno != 0 {
		return os.NewSyscallError(fmt.Sprintf("dm ioctl (cmd=0x%x)", cmd), errno)
	}

	return nil
}
