package manifester

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNarPathLess(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/", "/a", true},
		{"/a", "/", false},
		{"/a", "/b", true},
		{"/b", "/a", false},
		{"/a", "/a/b", true},
		{"/a/b", "/a", false},
		{"/a/b", "/a/c", true},
		{"/a/c", "/a/b", false},
		{"/a/b", "/b", true},
		{"/b", "/a/b", false},
		{"/a-b", "/a/b", false}, // "a" < "a-b", so "a/b" < "a-b"
		{"/a/b", "/a-b", true},
		{"/a/x", "/a-y", true},
		{"/a-y", "/a/x", false},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := narPathLess(tt.a, tt.b)
			assert.Equal(t, tt.want, got, "narPathLess(%q, %q)", tt.a, tt.b)
		})
	}
}
