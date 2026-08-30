package devmapper

import (
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
