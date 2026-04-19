package shift

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFileChunkSize(t *testing.T) {
	s := DefaultChunkShift // 16 -> 64 KiB
	sz := s.Size()

	tests := []struct {
		name      string
		totalSize int64
		isLast    bool
		want      int64
	}{
		{"zero file", 0, true, 0},
		{"single full chunk", sz, true, sz},
		{"multi full chunks - last", sz * 2, true, sz},
		{"multi full chunks - not last", sz * 2, false, sz},
		{"small chunk - last", sz / 2, true, sz / 2},
		{"large chunk - middle", sz * 3 / 2, false, sz},
		{"large chunk - last", sz * 3 / 2, true, sz / 2},
		{"exact multiple - last", sz * 10, true, sz},
		{"exact multiple - middle", sz * 10, false, sz},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.FileChunkSize(tt.totalSize, tt.isLast)
			assert.Equal(t, tt.want, got, "FileChunkSize(%d, %v)", tt.totalSize, tt.isLast)
		})
	}
}

func TestBlocks(t *testing.T) {
	s := DefaultChunkShift // 16 -> 64 KiB
	sz := s.Size()

	assert.Equal(t, int64(0), s.Blocks(0))
	assert.Equal(t, int64(1), s.Blocks(1))
	assert.Equal(t, int64(1), s.Blocks(sz))
	assert.Equal(t, int64(2), s.Blocks(sz+1))
	assert.Equal(t, int64(10), s.Blocks(sz*10))
}
