package devmapper

import (
	"strconv"
	"strings"
)

type CloneTable struct {
	Start       uint64
	Length      uint64
	MetaDev     string
	DestDev     string
	SourceDev   string
	RegionSize  uint64
	NoHydration bool
}

func (t CloneTable) start() uint64 {
	return t.Start
}

func (t CloneTable) length() uint64 {
	return t.Length
}

func (t CloneTable) targetType() string {
	return "clone"
}

func (t CloneTable) buildSpec() string {
	args := []string{
		t.MetaDev,
		t.DestDev,
		t.SourceDev,
		strconv.FormatUint(t.RegionSize/SectorSize, 10),
	}
	if t.NoHydration {
		args = append(args, "1", "no_hydration")
	}
	return strings.Join(args, " ")
}
