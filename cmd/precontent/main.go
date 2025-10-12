package main

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"os"
	"time"

	"github.com/lunixbochs/struc"
	"golang.org/x/sys/unix"
)

const NAME = "SPARSE"
const BASE = "BASE"

// const SIZE = 1 << 20

func check(msg string, err error) {
	if err != nil {
		log.Fatalln(msg, err)
	}
}

var basef, not *os.File
var basesz int64

func main() {
	log.Println("open base")
	var err error
	basef, err = os.Open(BASE)
	check("open", err)

	fi, err := basef.Stat()
	check("stat", err)
	basesz = fi.Size()

	log.Println("create sparse")
	f, err := os.OpenFile(NAME, os.O_RDWR|os.O_CREATE, 0666)
	check("create", err)
	check("trunc", f.Truncate(basesz))
	check("close", f.Close())

	log.Println("set up fanotify")
	fd, err := unix.FanotifyInit(
		unix.FAN_CLASS_PRE_CONTENT|
			unix.FAN_CLOEXEC|
			unix.FAN_NONBLOCK|
			unix.FAN_UNLIMITED_QUEUE,
		unix.O_WRONLY|
			unix.O_LARGEFILE|
			unix.O_CLOEXEC|
			unix.O_NOATIME,
	)
	check("FanotifyInit", err)
	log.Println("notify fd", fd)

	err = unix.FanotifyMark(
		fd,
		unix.FAN_MARK_ADD,
		unix.FAN_PRE_ACCESS,
		unix.AT_FDCWD,
		NAME,
	)
	check("FanotifyMark", err)

	// flags, err := unix.FcntlInt(uintptr(fd), syscall.F_GETFL, 0)
	// check("fcntl1", err)
	// log.Println("ISNONBLOCK0", flags&syscall.O_NONBLOCK != 0)

	not = os.NewFile(uintptr(fd), "<fanotify>")

	// flags, err = unix.FcntlInt(uintptr(fd), syscall.F_GETFL, 0)
	// check("fcntl2", err)
	// log.Println("ISNONBLOCK1", flags&syscall.O_NONBLOCK != 0)

	ch := make(chan []byte)

	for range 4 {
		go func() {
			for {
				buf := make([]byte, 256)
				n, err := not.Read(buf)
				log.Println("notify read", n, err)
				buf = buf[:n]
				for len(buf) >= 24 {
					n := binary.LittleEndian.Uint32(buf)
					ch <- buf[:n]
					buf = buf[n:]
				}
			}
		}()
	}

	for range 4 {
		go func() {
			for buf := range ch {
				_ = handleMessage(buf)
			}
		}()
	}

	time.Sleep(time.Hour)
}

func handleMessage(buf []byte) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("panic in handle:", r)
		}
		if retErr != nil {
			log.Println("handle error:", retErr)
		}
	}()

	var r bytes.Reader
	r.Reset(buf)

	// unpack metadata
	var msg fanotify_event_metadata
	if err := struc.UnpackWithOptions(&r, &msg, &_popts); err != nil {
		return err
	}
	// log.Printf("DEBUG EVENT %#v", msg)

	if msg.Vers != unix.FANOTIFY_METADATA_VERSION {
		return errors.New("fanotify metadata version mismatch")
	}

	defer func() {
		if msg.Fd >= 0 {
			_ = unix.Close(int(msg.Fd))
		}
	}()

	// unpack extras
	var rng fanotify_event_info_range
	for r.Len() > 0 {
		var infohdr fanotify_event_info_header
		if err := struc.UnpackWithOptions(&r, &infohdr, &_popts); err != nil {
			return err
		}
		switch infohdr.InfoType {
		case unix.FAN_EVENT_INFO_TYPE_RANGE:
			if infohdr.Len != 24 {
				log.Println("FAN_EVENT_INFO_TYPE_RANGE had wrong length", infohdr.Len)
			} else if err := struc.UnpackWithOptions(&r, &rng, &_popts); err != nil {
				return err
			}
		default:
			log.Println("unexpected fanotify info", infohdr)
			_, _ = r.Seek(int64(infohdr.Len)-4, io.SeekCurrent)
		}
	}

	if rng.Count == 0 {
		return errors.New("fanotify message did not contain range")
	}

	log.Println("RANGE", rng.Offset, rng.Count)

	defer func() {
		resp := fanotify_response{
			Fd:       msg.Fd,
			Response: unix.FAN_ALLOW,
		}
		var out bytes.Buffer
		out.Grow(8)
		check("pack", struc.PackWithOptions(&out, &resp, &_popts))
		_, wrErr := not.Write(out.Bytes())
		retErr = cmp.Or(retErr, wrErr)
	}()

	rng.Count = min(uint64(basesz)-rng.Offset, rng.Count)
	if rng.Count <= 0 {
		return nil
	}

	data := make([]byte, rng.Count)
	_, err := basef.ReadAt(data, int64(rng.Offset))
	check("pread", err)
	_, err = unix.Pwrite(int(msg.Fd), data, int64(rng.Offset))
	check("pwrite", err)

	return nil
}

var _popts = struc.Options{Order: binary.LittleEndian}
