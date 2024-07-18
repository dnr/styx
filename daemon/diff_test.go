package daemon

import (
	"testing"

	"github.com/dnr/styx/pb"
	"github.com/stretchr/testify/require"
)

var testEntries = []*pb.Entry{
	&pb.Entry{
		Path: "/",
	},
	&pb.Entry{
		Path:       "/inline",
		Size:       10,
		InlineData: []byte("hello file"),
	},
	&pb.Entry{
		Path:    "/file",
		Size:    1500,
		Digests: []byte("abcdefghIJKLMNOP"),
	},
	&pb.Entry{
		Path: "/dir",
	},
	&pb.Entry{
		Path:    "/dir/nextfile",
		Size:    500,
		Digests: []byte("11223344"),
	},
	&pb.Entry{
		Path:       "/symlink",
		Size:       7,
		InlineData: []byte("target!"),
	},
	&pb.Entry{
		Path:    "/lastfile",
		Size:    3000,
		Digests: []byte("888877776666555544443333"),
	},
}

func TestDigestIterator(t *testing.T) {
	r := require.New(t)
	s := &server{digestBytes: 8, chunkShift: 10}
	i := s.newDigestIterator(testEntries)

	r.Equal(testEntries[2], i.ent())
	r.Equal("abcdefgh", string(i.digest()))
	r.EqualValues(1024, i.size())

	r.True(i.next(1))
	r.Equal(testEntries[2], i.ent())
	r.Equal("IJKLMNOP", string(i.digest()))
	r.EqualValues(1500-1024, i.size())

	r.True(i.next(1))
	r.Equal(testEntries[4], i.ent())
	r.Equal("11223344", string(i.digest()))
	r.EqualValues(500, i.size())

	r.True(i.next(1))
	r.Equal(testEntries[6], i.ent())
	r.Equal("88887777", string(i.digest()))
	r.EqualValues(1024, i.size())

	r.True(i.next(2))
	r.Equal(testEntries[6], i.ent())
	r.Equal("44443333", string(i.digest()))
	r.EqualValues(3000-1024-1024, i.size())

	r.False(i.next(1))
}

func TestDigestIterator_FindFile(t *testing.T) {
	r := require.New(t)
	s := &server{digestBytes: 8, chunkShift: 10}
	i := s.newDigestIterator(testEntries)

	r.True(i.findFile("/dir/nextfile"))
	r.Equal(testEntries[4], i.ent())
	r.Equal("11223344", string(i.digest()))
	r.EqualValues(500, i.size())

	r.False(i.findFile("/symlink"), "false even though file is present")
	r.True(i.findFile("/lastfile"))
	r.False(i.findFile("/dir/nextfile"), "doesn't go backwards")
}

func TestDigestIterator_ToFileStart(t *testing.T) {
	r := require.New(t)
	s := &server{digestBytes: 8, chunkShift: 10}
	i := s.newDigestIterator(testEntries)

	r.True(i.next(4))
	r.Equal(testEntries[6], i.ent())
	r.Equal("66665555", string(i.digest()))
	r.EqualValues(1024, i.size())

	r.True(i.toFileStart())
	r.Equal(testEntries[6], i.ent())
	r.Equal("88887777", string(i.digest()))
	r.EqualValues(1024, i.size())
}
