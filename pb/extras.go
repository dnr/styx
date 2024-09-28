package pb

func (e *Entry) FileMode() int {
	if e.Executable {
		return 0o755
	}
	return 0o644
}
