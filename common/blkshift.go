package common

const ChunkShift BlkShift = 16

type BlkShift int

func (b BlkShift) Size() int64 {
	return 1 << b
}

func (b BlkShift) Roundup(i int64) int64 {
	m1 := b.Size() - 1
	return (i + m1) &^ m1
}

func (b BlkShift) Leftover(i int64) int64 {
	return i & (b.Size() - 1)
}

func (b BlkShift) Blocks(i int64) int64 {
	m1 := b.Size() - 1
	return (i + m1) >> b
}
