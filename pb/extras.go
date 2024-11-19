package pb

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

func (e *Entry) ChunkShiftDef() int {
	if e.ChunkShift == 0 {
		return 16
	}
	return int(e.ChunkShift)
}
