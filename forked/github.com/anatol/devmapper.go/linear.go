package devmapper

import (
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// LinearTable represents information needed for 'linear' target creation
type LinearTable struct {
	Start         uint64
	Length        uint64
	BackendDevice string
	BackendOffset uint64
}

func (l LinearTable) start() uint64 {
	return l.Start
}

func (l LinearTable) length() uint64 {
	return l.Length
}

func (l LinearTable) targetType() string {
	return "linear"
}

func (l LinearTable) buildSpec() string {
	args := []string{l.BackendDevice, strconv.FormatUint(l.BackendOffset/SectorSize, 10)}
	return strings.Join(args, " ")
}

type linearVolume struct {
	f      *os.File
	offset int64
}

func (l LinearTable) openVolume(flag int, perm fs.FileMode) (Volume, error) {
	file, err := os.OpenFile(l.BackendDevice, flag, perm)
	if err != nil {
		return nil, err
	}
	return &linearVolume{f: file, offset: int64(l.BackendOffset)}, nil
}

func (l linearVolume) ReadAt(buf []byte, off int64) (n int, err error) {
	return l.f.ReadAt(buf, off+l.offset)
}

func (l linearVolume) WriteAt(buf []byte, off int64) (n int, err error) {
	return l.f.WriteAt(buf, off+l.offset)
}

func (l linearVolume) Close() error {
	return l.f.Close()
}
