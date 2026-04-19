package shift

const (
	ManifestChunkShift Shift = 16
	DefaultChunkShift  Shift = 16
	MaxChunkShift      Shift = 20
)

type Shift int

func (b Shift) Size() int64 {
	return 1 << b
}

func (b Shift) Roundup(i int64) int64 {
	m1 := b.Size() - 1
	return (i + m1) &^ m1
}

func (b Shift) Leftover(i int64) int64 {
	return i & (b.Size() - 1)
}

func (b Shift) Blocks(i int64) int64 {
	m1 := b.Size() - 1
	return (i + m1) >> b
}

func (b Shift) FileChunkSize(totalSize int64, isLast bool) int64 {
	if totalSize == 0 {
		return 0 // we shouldn't get here
	} else if !isLast {
		return b.Size()
	} else if left := b.Leftover(totalSize); left > 0 {
		return left
	}
	return b.Size() // exact multiple of chunk size
}
