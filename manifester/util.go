package manifester

import "io"

type countWriter struct {
	w io.Writer
	c int
}

func (c *countWriter) Write(p []byte) (n int, err error) {
	c.c += len(p)
	return c.w.Write(p)
}
