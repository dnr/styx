package styx

type (
	blkshift int
)

func (b blkshift) size() int64 {
	return 1 << int(b)
}

func (b blkshift) roundup(i int64) int64 {
	m1 := b.size() - 1
	return (i + m1) & ^m1
}

func (b blkshift) leftover(i int64) int64 {
	return i & (b.size() - 1)
}
