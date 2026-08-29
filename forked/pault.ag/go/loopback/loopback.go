// {{{ Copyright (c) Paul R. Tagliamonte <paultag@gmail.com> 2017-2021
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE. }}}

package loopback

import (
	"fmt"
	"os"
	"syscall"
)

const (
	// loopSetFd will associate the loop device with the open file
	loopSetFd = 0x4C00

	// loopCtlGetFree will allocate or find a free loop device for use.
	loopCtlGetFree = 0x4C82

	// loopGetStatus64 will get the status of the loop device.
	loopGetStatus64 = 0x4C05

	// loopSetStatus64 will set the status of the loop device.
	loopSetStatus64 = 0x4C04

	// loopClrFd will disassociate the loop device from any file descriptor.
	loopClrFd = 0x4C01
)

const (
	// loFlagsAutoClear will instruct the kernel to autodestruct on last close.
	loFlagsAutoClear = 4

	// loFlagsReadOnly requests the loopback device be read-only.
	loFlagsReadOnly = 1
)

// syscalls will return an errno type (which implements error) for all calls,
// including success (errno 0). We only care about non-zero errnos.
func errnoIsErr(err error) error {
	if err.(syscall.Errno) != 0 {
		return err
	}
	return nil
}

// Loop will, when given a handle to a Loopback device (such as /dev/loop0),
// and a handle to the fs image to loop mount (such as a squashfs or ext4fs
// image), preform the required call to loop the image to the provided block
// device.
//
// The first argument (loopbackDevice) can be obtained using
// loopback.NextLoopDevice, if one is not known in advance.
func Loop(loopbackDevice, image *os.File) error {
	_, _, err := syscall.Syscall(
		syscall.SYS_IOCTL,
		loopbackDevice.Fd(),
		loopSetFd,
		image.Fd(),
	)
	return errnoIsErr(err)
}

// Unloop will, given a handle to the Loopback device (such as /dev/loop0),
// preform the required call to the image to unloop the image mounted at
// that location.
func Unloop(loopbackDevice *os.File) error {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, loopbackDevice.Fd(), loopClrFd, 0)
	return errnoIsErr(err)
}

// NextLoopDevice will return the next loopback device that isn't used. Under
// the hood this will ask loop-control for the LOOP_CTL_GET_FREE value, and
// interpolate that into the conventional GNU/Linux naming scheme for loopback
// devices, and os.Open that path.
func NextLoopDevice() (*os.File, error) {
	loopInt, err := nextUnallocatedLoop()
	if err != nil {
		return nil, err
	}
	return os.OpenFile(fmt.Sprintf("/dev/loop%d", loopInt), os.O_RDWR, 0)
}

// nextUnallocatedLoop will return the integer of the next loopback device we
// can use by calling loop-control with the LOOP_CTL_GET_FREE ioctl.
func nextUnallocatedLoop() (int, error) {
	fd, err := os.OpenFile("/dev/loop-control", os.O_RDONLY, 0644)
	if err != nil {
		return 0, err
	}
	defer fd.Close()
	index, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd.Fd(), loopCtlGetFree, 0)
	return int(index), errnoIsErr(err)
}
