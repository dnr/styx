package styx

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"path"
	"sort"

	"github.com/lunixbochs/struc"
	"github.com/nix-community/go-nix/pkg/nar"
	"golang.org/x/sys/unix"
)

type (
	builder struct {
		err error
		nar *nar.Reader
		out io.Writer

		blk blkshift
	}

	regulardata struct {
		i    *inodebuilder
		data []byte
	}

	inodebuilder struct {
		i        erofs_inode_compact
		taildata []byte
		nid      uint64
	}

	dbent struct {
		name string
		i    *inodebuilder
		tp   uint16
	}

	dirbuilder struct {
		ents []dbent
		i    *inodebuilder
		size int64
	}
)

func NewBuilder(r io.Reader, out io.Writer) *builder {
	nr, err := nar.NewReader(r)
	if err != nil {
		return &builder{err: err}
	}
	return &builder{nar: nr, out: out, blk: blkshift(12)}
}

func (b *builder) Build() error {
	if b.err != nil {
		return b.err
	}

	const inodeshift = blkshift(5)
	const inodesize = 1 << inodeshift
	const layoutCompact = EROFS_INODE_LAYOUT_COMPACT << EROFS_I_VERSION_BIT
	const formatPlain = (layoutCompact | (EROFS_INODE_FLAT_PLAIN << EROFS_I_DATALAYOUT_BIT))
	const formatInline = (layoutCompact | (EROFS_INODE_FLAT_INLINE << EROFS_I_DATALAYOUT_BIT))

	var inodes []*inodebuilder
	var root *inodebuilder
	var datablocks []regulardata
	dirsmap := make(map[string]*dirbuilder)
	var dirs []*dirbuilder

	allowedTail := func(tail int64) bool {
		// tail has to fit in a block with the inode
		return tail > 0 && tail <= b.blk.size()-inodesize
	}
	setDataOnInode := func(i *inodebuilder, data []byte) {
		tail := b.blk.leftover(int64(len(data)))
		i.i.ISize = truncU32(len(data))
		if !allowedTail(tail) {
			tail = 0
			i.i.IFormat = formatPlain
		}
		full := int64(len(data)) - tail
		if full > 0 {
			datablocks = append(datablocks, regulardata{
				i:    i,
				data: data[:full],
			})
		}
		i.taildata = data[full:]
	}

	// pass 1: read nar

	for {
		h, err := b.nar.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		} else if err = h.Validate(); err != nil {
			return err
		} else if h.Path == "/" && h.Type != nar.TypeDirectory {
			return errors.New("can't handle bare file nars yet")
		}

		// every header gets an inode and all but the root get a dirent
		var fstype uint16
		i := &inodebuilder{
			i: erofs_inode_compact{
				IFormat: formatInline,
				IIno:    truncU32(len(inodes) + 37),
				INlink:  1,
			},
		}
		inodes = append(inodes, i)

		switch h.Type {
		case nar.TypeDirectory:
			fstype = EROFS_FT_DIR
			i.i.IMode = unix.S_IFDIR | 0755
			i.i.INlink = 2
			parent := i
			if h.Path != "/" {
				parent = dirsmap[path.Dir(h.Path)].i
			}
			db := &dirbuilder{
				ents: []dbent{
					{name: ".", tp: EROFS_FT_DIR, i: i},
					{name: "..", tp: EROFS_FT_DIR, i: parent},
				},
				i: i,
			}
			dirs = append(dirs, db)
			dirsmap[h.Path] = db

		case nar.TypeRegular:
			fstype = EROFS_FT_REG_FILE
			i.i.IMode = unix.S_IFREG | 0644
			if h.Executable {
				i.i.IMode = unix.S_IFREG | 0755
			}
			if h.Size > 0 {
				if h.Size > math.MaxUint32 {
					return fmt.Errorf("TODO: support larger files with extended inode")
				}
				data, err := readFullFromNar(b.nar, h)
				if err != nil {
					return err
				}
				setDataOnInode(i, data)
			}

		case nar.TypeSymlink:
			fstype = EROFS_FT_SYMLINK
			i.i.IMode = unix.S_IFLNK | 0777
			i.i.ISize = truncU32(len(h.LinkTarget))
			i.taildata = []byte(h.LinkTarget)

		default:
			return errors.New("unknown type")
		}

		// add dirent to appropriate directory
		if h.Path == "/" {
			root = i
		} else {
			dir, file := path.Split(h.Path)
			db := dirsmap[path.Clean(dir)]
			db.ents = append(db.ents, dbent{
				name: file,
				i:    i,
				tp:   fstype,
			})
		}
	}

	// pass 2: pack inodes and tails

	// 2.1: directory sizes
	dummy := make([]byte, b.blk.size())
	for _, db := range dirs {
		db.sortAndSize(b.blk)
		tail := b.blk.leftover(db.size)
		if !allowedTail(tail) {
			tail = 0
		}
		// dummy data just to track size
		db.i.taildata = dummy[:tail]
	}

	// at this point:
	// all inodes have correct len(taildata)
	// files have correct taildata but dirs do not

	inodebase := max(4096, b.blk.size())

	// 2.2: lay out inodes and tails
	// using greedy for now. TODO: use best fit or something
	p := int64(0) // position relative to metadata area
	remaining := b.blk.size()
	for i, inode := range inodes {
		need := inodesize + inodeshift.roundup(int64(len(inode.taildata)))
		if need > remaining {
			p += remaining
			remaining = b.blk.size()
		}
		inodes[i].nid = truncU64(p >> inodeshift)
		p += need
		remaining -= need
	}
	if remaining < b.blk.size() {
		p += remaining
	}

	// at this point:
	// all inodes have correct nid

	// 2.3: write actual dirs to buffers
	for _, db := range dirs {
		var buf bytes.Buffer
		db.write(&buf, b.blk)
		if int64(buf.Len()) != db.size {
			panic("oops, bug")
		}
		setDataOnInode(db.i, buf.Bytes())
	}

	// at this point:
	// all inodes have correct taildata

	// from here on, p is relative to 0
	p += inodebase

	// 2.4: lay out full blocks
	for _, data := range datablocks {
		data.i.i.IU = truncU32(p >> b.blk)
		p += b.blk.roundup(int64(len(data.data)))
	}
	fmt.Printf("final calculated size %d\n", p)

	// at this point:
	// all inodes have correct IU and ISize

	// pass 3: write

	// 3.1: fill in super
	super := erofs_super_block{
		Magic:       0xE0F5E1E2,
		BlkSzBits:   truncU8(b.blk),
		RootNid:     truncU16(root.nid),
		Inos:        truncU64(len(inodes)),
		Blocks:      truncU32(p >> b.blk),
		MetaBlkAddr: truncU32(inodebase >> b.blk),
	}
	// TODO: make uuid from nar hash to be reproducible
	rand.Read(super.Uuid[:])
	// TODO: append a few bits of nar hash to volname
	copy(super.VolumeName[:], "styx-test")

	c := crc32.NewIEEE()
	pack(c, &super)
	super.Checksum = c.Sum32()

	pad(b.out, 1024)
	pack(b.out, &super)
	pad(b.out, inodebase-1024-128)

	// 3.2: inodes and tails
	remaining = b.blk.size()
	for _, i := range inodes {
		need := inodesize + inodeshift.roundup(int64(len(i.taildata)))
		if need > remaining {
			pad(b.out, remaining)
			remaining = b.blk.size()
		}
		pack(b.out, i.i)
		writeAndPad(b.out, i.taildata, inodeshift)
		remaining -= need
	}
	if remaining < b.blk.size() {
		pad(b.out, remaining)
	}

	// 3.4: full blocks
	for _, data := range datablocks {
		writeAndPad(b.out, data.data, b.blk)
	}

	return nil
}

func (db *dirbuilder) sortAndSize(shift blkshift) {
	const direntsize = 12

	sort.Slice(db.ents, func(i, j int) bool { return db.ents[i].name < db.ents[j].name })

	blocks := int64(0)
	remaining := shift.size()

	for _, ent := range db.ents {
		need := int64(direntsize + len(ent.name))
		if need > remaining {
			blocks++
			remaining = shift.size()
		}
		remaining -= need
	}

	db.size = blocks<<shift + (shift.size() - remaining)
}

func (db *dirbuilder) write(out io.Writer, shift blkshift) {
	const direntsize = 12

	remaining := shift.size()
	ents := make([]erofs_dirent, 0, shift.size()/16)
	var names bytes.Buffer

	flush := func(isTail bool) {
		nameoff0 := truncU16(len(ents) * direntsize)
		for i := range ents {
			ents[i].NameOff += nameoff0
		}
		pack(out, ents)
		io.Copy(out, &names)
		if !isTail {
			pad(out, remaining)
		}

		ents = ents[:0]
		names.Reset()
		remaining = shift.size()
	}

	for _, ent := range db.ents {
		need := int64(direntsize + len(ent.name))
		if need > remaining {
			flush(false)
		}

		ents = append(ents, erofs_dirent{
			Nid:      ent.i.nid,
			NameOff:  truncU16(names.Len()), // offset minus nameoff0
			FileType: truncU8(ent.tp),
		})
		names.Write([]byte(ent.name))
		remaining -= need
	}
	flush(true)

	return
}

var _zeros = make([]byte, 4096)

func pad(out io.Writer, n int64) error {
	for {
		if n <= 4096 {
			_, err := out.Write(_zeros[:n])
			return err
		}
		out.Write(_zeros)
		n -= 4096
	}
}

var _popts = struc.Options{Order: binary.LittleEndian}

func pack(out io.Writer, v any) error {
	return struc.PackWithOptions(out, v, &_popts)
}

func writeAndPad(out io.Writer, data []byte, shift blkshift) error {
	n, err := out.Write(data)
	r := shift.roundup(int64(n)) - int64(n)
	pad(out, r)
	return err
}

func readFullFromNar(nr *nar.Reader, h *nar.Header) ([]byte, error) {
	buf := make([]byte, h.Size)
	num, err := io.ReadFull(nr, buf)
	if err != nil {
		return nil, err
	} else if num != int(h.Size) {
		return nil, io.ErrUnexpectedEOF
	}
	return buf, nil
}
