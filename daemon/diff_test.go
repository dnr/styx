package daemon

import (
	"testing"

	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/pb"
	"github.com/stretchr/testify/require"
)

var d1 = "0123456789ytrewq6789poiu"
var d2 = "hjkl30104mnop410019jjkka"
var d3 = "zxcv95345asdfiijb632ooia"
var testCShift shift.Shift = 16 // TODO: test with different sizes
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
		Size:    testCShift.Size() + 100,
		Digests: []byte(d1 + d2),
	},
	&pb.Entry{
		Path: "/dir",
	},
	&pb.Entry{
		Path:    "/dir/nextfile",
		Size:    500,
		Digests: []byte(d3),
	},
	&pb.Entry{
		Path:       "/symlink",
		Size:       7,
		InlineData: []byte("target!"),
	},
	&pb.Entry{
		Path:    "/lastfile",
		Size:    testCShift.Size()*2 + 300,
		Digests: []byte(d3 + d2 + d1),
	},
}

func fb(s string) cdig.CDig {
	return cdig.FromBytes([]byte(s))
}

func TestDigestIterator(t *testing.T) {
	r := require.New(t)
	i := newDigestIterator(testEntries)

	r.Equal(testEntries[2], i.ent())
	r.Equal(fb(d1), i.digest())
	r.EqualValues(testCShift.Size(), i.size())

	r.NotNil(i.next(1))
	r.Equal(testEntries[2], i.ent())
	r.Equal(fb(d2), i.digest())
	r.EqualValues(100, i.size())

	r.NotNil(i.next(1))
	r.Equal(testEntries[4], i.ent())
	r.Equal(fb(d3), i.digest())
	r.EqualValues(500, i.size())

	r.NotNil(i.next(1))
	r.Equal(testEntries[6], i.ent())
	r.Equal(fb(d3), i.digest())
	r.EqualValues(testCShift.Size(), i.size())

	r.NotNil(i.next(2))
	r.Equal(testEntries[6], i.ent())
	r.Equal(fb(d1), i.digest())
	r.EqualValues(300, i.size())

	r.Nil(i.next(1))
}

func TestDigestIterator_FindFile(t *testing.T) {
	r := require.New(t)
	i := newDigestIterator(testEntries)

	r.True(i.findFile("/dir/nextfile"))
	r.Equal(testEntries[4], i.ent())
	r.Equal(fb(d3), i.digest())
	r.EqualValues(500, i.size())

	r.False(i.findFile("/symlink"), "false even though file is present")
	r.True(i.findFile("/lastfile"))
	r.False(i.findFile("/dir/nextfile"), "doesn't go backwards")
}

func TestDigestIterator_ToFileStart(t *testing.T) {
	r := require.New(t)
	i := newDigestIterator(testEntries)

	r.NotNil(i.next(4))
	r.Equal(testEntries[6], i.ent())
	r.Equal(fb(d2), i.digest())
	r.EqualValues(testCShift.Size(), i.size())

	r.True(i.toFileStart())
	r.Equal(testEntries[6], i.ent())
	r.Equal(fb(d3), i.digest())
	r.EqualValues(testCShift.Size(), i.size())
}
