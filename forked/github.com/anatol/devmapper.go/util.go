package devmapper

import "bytes"

func roundUp(n int, divider int) int {
	return (n + divider - 1) / divider * divider
}

func fixedArrayToString(buff []byte) string {
	idx := bytes.IndexByte(buff, 0)
	if idx != -1 {
		buff = buff[:idx]
	}
	return string(buff)
}
