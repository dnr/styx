package pb

import "github.com/dnr/styx/common/shift"

func (e *Entry) FileMode() int {
	if e.Executable {
		return 0o755
	}
	return 0o644
}

func (e *Entry) DigestBytesDef() int {
	if e.DigestBytes == 0 {
		return 24
	}
	return int(e.DigestBytes)
}

func (e *Entry) ChunkShiftDef() shift.Shift {
	if e.ChunkShift == 0 {
		return shift.DefaultChunkShift
	}
	return shift.Shift(e.ChunkShift)
}

func (e *Entry) Chunks() int {
	return len(e.Digests) / e.DigestBytesDef()
}
