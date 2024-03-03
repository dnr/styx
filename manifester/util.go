package manifester

// TODO: replace with cmp.Or after go1.22
// Or returns the first of its arguments that is not equal to the zero value.
// If no argument is non-zero, it returns the zero value.
func Or[T comparable](vals ...T) T {
	var zero T
	for _, val := range vals {
		if val != zero {
			return val
		}
	}
	return zero
}

type (
	blkshift int
)

func (b blkshift) size() int64 {
	return 1 << b
}

func (b blkshift) roundup(i int64) int64 {
	m1 := b.size() - 1
	return (i + m1) & ^m1
}

func (b blkshift) leftover(i int64) int64 {
	return i & (b.size() - 1)
}
