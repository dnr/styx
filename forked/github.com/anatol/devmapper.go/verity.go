package devmapper

import (
	"io/fs"
	"strconv"
	"strings"
)

// VerityTable represents information needed for 'verity' target creation
type VerityTable struct {
	Start                         uint64
	Length                        uint64
	HashType                      uint64
	DataDevice                    string // the device containing data, the integrity of which needs to be checked
	HashDevice                    string // device that supplies the hash tree data
	DataBlockSize, HashBlockSize  uint64
	NumDataBlocks, HashStartBlock uint64
	Algorithm, Digest, Salt       string
	Params                        []string
}

func (v VerityTable) start() uint64 {
	return v.Start
}

func (v VerityTable) length() uint64 {
	return v.Length
}

func (v VerityTable) targetType() string {
	return "verity"
}

func (v VerityTable) buildSpec() string {
	args := []string{
		strconv.FormatUint(v.HashType, 10), v.DataDevice, v.HashDevice, strconv.FormatUint(v.DataBlockSize, 10),
		strconv.FormatUint(v.HashBlockSize, 10), strconv.FormatUint(v.NumDataBlocks, 10),
		strconv.FormatUint(v.HashStartBlock, 10), v.Algorithm, v.Digest, v.Salt,
	}

	args = append(args, v.Params...)
	return strings.Join(args, " ")
}

type verityVolume struct{}

func (v VerityTable) openVolume(flag int, perm fs.FileMode) (Volume, error) {
	return &verityVolume{}, nil
}

func (v verityVolume) ReadAt(p []byte, off int64) (n int, err error) {
	return 0, errNotImplemented
}

func (v verityVolume) WriteAt(p []byte, off int64) (n int, err error) {
	return 0, errNotImplemented
}

func (v verityVolume) Close() error {
	return errNotImplemented
}
