package daemon

import (
	"errors"
	"sync"

	"github.com/dnr/styx/common"
	"github.com/freddierice/go-losetup/v2"
	"golang.org/x/sys/unix"
)

type locache struct {
	lock      sync.Mutex
	pathToDev map[string]losetup.Device
	devToPath map[losetup.Device]string
}

var invalidLo = losetup.New(99999, -99999)

func newLoCache() *locache {
	return &locache{
		pathToDev: make(map[string]losetup.Device),
		devToPath: make(map[losetup.Device]string),
	}
}

func (l *locache) init() {
	l.lock.Lock()
	defer l.lock.Unlock()

	for i := 0; ; i++ {
		lo := losetup.New(uint64(i), 0)
		// open manually since the library swallows the error
		fd, err := unix.Open(lo.Path(), unix.O_RDONLY, 0)
		if errors.Is(err, unix.ENOENT) && i >= 16 {
			break
		} else if err != nil {
			continue
		}
		_ = unix.Close(fd)

		info, err := lo.GetInfo()
		if err != nil {
			continue
		}
		path := common.StringFromFixedBytes(info.FileName[:])
		l.pathToDev[path] = lo
		l.devToPath[lo] = path
	}
}

func (l *locache) findOrAttach(path string) (losetup.Device, error) {
	l.lock.Lock()
	defer l.lock.Unlock()

	if lo, ok := l.pathToDev[path]; ok {
		return lo, nil
	}

	lo, err := losetup.Attach(path, 0, false)
	if err != nil {
		return invalidLo, err
	}

	l.pathToDev[path] = lo
	l.devToPath[lo] = path
	return lo, nil
}

func (l *locache) detach(lo losetup.Device) error {
	l.lock.Lock()
	defer l.lock.Unlock()

	if err := lo.Detach(); err != nil {
		return err
	}

	if path, ok := l.devToPath[lo]; ok {
		delete(l.pathToDev, path)
	}
	delete(l.devToPath, lo)
	return nil
}
