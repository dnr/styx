package common

import "github.com/dnr/styx/common/shift"

func PickChunkShift(fileSize int64) shift.Shift {
	// aim for 64-256 chunks/file
	switch {
	case fileSize <= 256<<16: // 16 MiB
		return 16
	case fileSize <= 256<<18: // 64 MiB
		return 18
	default:
		return shift.MaxChunkShift
	}
}
