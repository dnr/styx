package styx

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"

	"github.com/lunixbochs/struc"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

type (
	server struct {
		cfg *config
		db  *bbolt.DB
	}
)

func CachefilesServer() *server {
	cfg := loadConfig()
	return &server{cfg: cfg}
}

func (s *server) openDb() (err error) {
	opts := bbolt.Options{
		NoFreelistSync: true,
		FreelistType:   bbolt.FreelistMapType,
	}
	s.db, err = bbolt.Open(path.Join(s.cfg.CachePath, dbFilename), 0644, &opts)
	return
}

func (s *server) setupEnv() error {
	err := exec.Command("modprobe", "cachefiles").Run()
	if err != nil {
		return err
	}
	return os.MkdirAll(s.cfg.CachePath, 0755)
}

func (s *server) openDevNode() (devnode int, err error) {
	devnode, err = unix.Open(s.cfg.DevPath, unix.O_RDWR, 0600)
	if err == unix.ENOENT {
		_ = unix.Mknod(s.cfg.DevPath, 0600|unix.S_IFCHR, 10*256+122)
		devnode, err = unix.Open(s.cfg.DevPath, unix.O_RDWR, 0600)
	}
	return
}

func (s *server) Run() error {
	if err := s.setupEnv(); err != nil {
		return err
	}
	if err := s.openDb(); err != nil {
		return err
	}

	devnode, err := s.openDevNode()
	if err != nil {
		return err
	}

	if _, err = unix.Write(devnode, []byte("dir "+s.cfg.CachePath)); err != nil {
		return err
	} else if _, err = unix.Write(devnode, []byte("tag "+cacheTag)); err != nil {
		return err
	} else if _, err = unix.Write(devnode, []byte("bind ondemand")); err != nil {
		return err
	}

	buf := make([]byte, CACHEFILES_MSG_MAX_SIZE)

	for {
		fds := []unix.PollFd{{
			Fd:     int32(devnode),
			Events: unix.POLLIN,
		}}
		n, err := unix.Poll(fds, 1000*1000)
		if err != nil {
			return err
		}
		if n == 1 {
			n, err := unix.Read(devnode, buf)
			if err != nil {
				return err
			}
			err = s.handleMessage(buf[:n])
			if err != nil {
				log.Printf("error handling message: %v", err)
			}
		}
	}
	return nil
}

func (s *server) handleMessage(buf []byte) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic: %v", r)
		}
	}()

	r := bytes.NewReader(buf)
	var msg cachefiles_msg
	if err := struc.Unpack(r, &msg); err != nil {
		return err
	}
	switch msg.OpCode {
	case CACHEFILES_OP_OPEN:
		var open cachefiles_open
		if err := struc.Unpack(r, &open); err != nil {
			return err
		}
		return s.handleOpen(msg.MsgId, msg.ObjectId, open.Fd, open.Flags, open.VolumeKey, open.CookieKey)
	case CACHEFILES_OP_CLOSE:
		return s.handleClose(msg.MsgId, msg.ObjectId)
	case CACHEFILES_OP_READ:
		var read cachefiles_read
		if err := struc.Unpack(r, &read); err != nil {
			return err
		}
		return s.handleRead(msg.MsgId, msg.ObjectId, read.Len, read.Off)
	default:
		return errors.New("unknown opcode")
	}
}

func (s *server) handleOpen(msgId, objectId, fd, flags uint32, volume, cookie []byte) error {
	// volume is "erofs,<domain_id>" (domain_id is same as fsid if not specified)
	// cookie is "<fsid>"
	log.Println("OPEN", msgId, objectId, fd, flags, string(volume), string(cookie))
	panic("unimpl")
}

func (s *server) handleClose(msgId, objectId uint32) error {
	log.Println("CLOSE", msgId, objectId)
	panic("unimpl")
}

func (s *server) handleRead(msgId, objectId uint32, ln, off uint64) error {
	log.Println("READ", msgId, objectId, ln, off)
	panic("unimpl")
}
