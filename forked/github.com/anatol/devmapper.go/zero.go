package devmapper

import "io/fs"

// ZeroTable represents information needed for 'zero' target creation
type ZeroTable struct {
	Start  uint64
	Length uint64
}

func (z ZeroTable) start() uint64 {
	return z.Start
}

func (z ZeroTable) length() uint64 {
	return z.Length
}

func (z ZeroTable) buildSpec() string {
	return ""
}

func (z ZeroTable) targetType() string {
	return "zero"
}

type zeroVolume struct{}

func (z ZeroTable) openVolume(flag int, perm fs.FileMode) (Volume, error) {
	return &zeroVolume{}, nil
}

func (z zeroVolume) ReadAt(p []byte, off int64) (n int, err error) {
	return len(p), nil
}

func (z zeroVolume) WriteAt(p []byte, off int64) (n int, err error) {
	return len(p), nil
}

func (z zeroVolume) Close() error {
	return nil
}
