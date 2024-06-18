package ci

func batches[T any, S []T](s S, size int) [][]T {
	var out [][]T
	i := 0
	for i < len(s) {
		end := min(i+size, len(s))
		out = append(out, s[i:end])
		i = end
	}
	return out
}
