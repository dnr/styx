package common

func PickChunkShift(fileSize int64) BlkShift {
	// aim for 64-256 chunks/file
	switch {
	case fileSize <= 256<<16: // 16 MiB
		return 16
	case fileSize <= 256<<18: // 64 MiB
		return 18
	default:
		return MaxChunkShift
	}
}
