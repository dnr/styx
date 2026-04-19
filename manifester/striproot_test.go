package manifester

import (
	"testing"

	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/stretchr/testify/assert"
)

func TestStripRoot(t *testing.T) {
	tests := []struct {
		name string
		ents []*tarEntry
		want []*tarEntry
	}{
		{
			name: "no contents",
			ents: []*tarEntry{
				{Header: nar.Header{Path: "/", Type: nar.TypeDirectory}},
			},
			want: []*tarEntry{
				{Header: nar.Header{Path: "/", Type: nar.TypeDirectory}},
			},
		},
		{
			name: "strip",
			ents: []*tarEntry{
				{Header: nar.Header{Path: "/", Type: nar.TypeDirectory}},
				{Header: nar.Header{Path: "/root", Type: nar.TypeDirectory}},
				{Header: nar.Header{Path: "/root/a", Type: nar.TypeRegular}},
			},
			want: []*tarEntry{
				{Header: nar.Header{Path: "/", Type: nar.TypeDirectory}},
				{Header: nar.Header{Path: "/a", Type: nar.TypeRegular}},
			},
		},
		{
			name: "not a dir",
			ents: []*tarEntry{
				{Header: nar.Header{Path: "/a", Type: nar.TypeRegular}},
			},
			want: []*tarEntry{
				{Header: nar.Header{Path: "/a", Type: nar.TypeRegular}},
			},
		},
		{
			name: "multiple dirs",
			ents: []*tarEntry{
				{Header: nar.Header{Path: "/", Type: nar.TypeDirectory}},
				{Header: nar.Header{Path: "/a", Type: nar.TypeDirectory}},
				{Header: nar.Header{Path: "/b", Type: nar.TypeDirectory}},
			},
			want: []*tarEntry{
				{Header: nar.Header{Path: "/", Type: nar.TypeDirectory}},
				{Header: nar.Header{Path: "/a", Type: nar.TypeDirectory}},
				{Header: nar.Header{Path: "/b", Type: nar.TypeDirectory}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripRoot(tt.ents)
			assert.Equal(t, len(tt.want), len(got))
			for i := range got {
				assert.Equal(t, tt.want[i].Path, got[i].Path)
				assert.Equal(t, tt.want[i].Type, got[i].Type)
			}
		})
	}
}
