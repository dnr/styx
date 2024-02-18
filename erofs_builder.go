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

		blkshift blkshift
	}

	regulardata struct {
		inum uint32
		data []byte
	}

	dbent struct {
		name string
		inum uint32
		tp   uint16
	}

	dirbuilder struct {
		ents []dbent
		inum uint32
	}
)

func NewBuilder(r io.Reader, out io.Writer) *builder {
	nr, err := nar.NewReader(r)
	if err != nil {
		return &builder{err: err}
	}
	return &builder{nar: nr, out: out, blkshift: blkshift(12)}
}

func (b *builder) Build() error {
	if b.err != nil {
		return b.err
	}

	var root uint32
	var inodes []erofs_inode_compact // note inodes[0].inum = 1
	var datablocks []regulardata
	dirsmap := make(map[string]*dirbuilder)
	var dirs []*dirbuilder

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

		// FIXME: make this work
		if h.Type == nar.TypeSymlink {
			continue
		}

		// every header gets an inode and all but the root get a dirent
		var fstype uint16
		inum := uint32(len(inodes) + 1)
		i := erofs_inode_compact{
			IFormat: ((EROFS_INODE_LAYOUT_COMPACT << EROFS_I_VERSION_BIT) |
				(EROFS_INODE_FLAT_PLAIN << EROFS_I_DATALAYOUT_BIT)),
			IIno:   inum,
			INlink: 1,
		}

		switch h.Type {
		case nar.TypeDirectory:
			fstype = EROFS_FT_DIR
			i.IMode = unix.S_IFDIR | 0755
			i.INlink = 2
			parent := inum
			if h.Path != "/" {
				parent = dirsmap[path.Dir(h.Path)].inum
			}
			db := &dirbuilder{
				ents: []dbent{
					{name: ".", tp: EROFS_FT_DIR, inum: inum},
					{name: "..", tp: EROFS_FT_DIR, inum: parent},
				},
				inum: inum,
			}
			dirs = append(dirs, db)
			dirsmap[h.Path] = db
		case nar.TypeRegular:
			fstype = EROFS_FT_REG_FILE
			i.IMode = unix.S_IFREG | 0644
			if h.Executable {
				i.IMode = unix.S_IFREG | 0755
			}
			if i.ISize > math.MaxUint32 {
				return fmt.Errorf("TODO: support larger files with extended inode")
			}
			i.ISize = uint32(h.Size)
			data := make([]byte, h.Size)
			n, err := b.nar.Read(data)
			if err != nil {
				return err
			} else if n < int(h.Size) {
				return errors.New("short read")
			}
			datablocks = append(datablocks, regulardata{
				inum: inum,
				data: data,
			})
		case nar.TypeSymlink:
			fstype = EROFS_FT_SYMLINK
			i.IMode = unix.S_IFLNK | 0777
			// FIXME: contents!! tail pack?
		default:
			return errors.New("unknown type")
		}

		inodes = append(inodes, i)

		// add dirent to appropriate directory
		if h.Path == "/" {
			root = inum
		} else {
			dir, file := path.Split(h.Path)
			db := dirsmap[path.Clean(dir)]
			db.ents = append(db.ents, dbent{
				name: file,
				inum: inum,
				tp:   fstype,
			})
		}
	}

	// pass 2: calculate dir sizes, offsets, etc.

	// layout: inodes start at 4096 or first block
	// all inodes in a row, then all dirs, then all regular files

	const inodesize = 32
	inodebase := max(4096, b.blkshift.size())
	inodelen := b.blkshift.roundup(int64(len(inodes)) * inodesize)
	dirbase := inodebase + inodelen
	p := dirbase
	for _, db := range dirs {
		blockaddr := p >> b.blkshift
		inodes[db.inum-1].IU = uint32(blockaddr) // FIXME: check overflow
		size := db.size(b.blkshift)
		inodes[db.inum-1].ISize = uint32(size)
		p += b.blkshift.roundup(size)
	}
	for _, data := range datablocks {
		blockaddr := p >> b.blkshift
		inodes[data.inum-1].IU = uint32(blockaddr) // FIXME: check overflow
		p += b.blkshift.roundup(int64(len(data.data)))
	}

	// pass 3: write

	// fill in super
	super := erofs_super_block{
		Magic:       0xE0F5E1E2,
		BlkSzBits:   uint8(b.blkshift),
		RootNid:     uint16(root - 1),
		Inos:        uint64(len(inodes)),
		Blocks:      uint32(p >> b.blkshift),
		MetaBlkAddr: uint32(inodebase >> b.blkshift),
	}
	// TODO: make uuid from nar hash to be reproducible
	rand.Read(super.Uuid[:])
	// TODO: append a few bits of nar hash to volname
	copy(super.VolumeName[:], "styx-test")

	c := crc32.NewIEEE()
	pack(c, &super)
	super.Checksum = c.Sum32()

	// super
	pad(b.out, 1024)
	pack(b.out, &super)
	pad(b.out, inodebase-1024-128)
	// inodes
	pack(b.out, inodes)
	pad(b.out, inodelen-inodesize*int64(len(inodes)))
	// dirs
	for _, db := range dirs {
		db.write(b.out, b.blkshift)
	}
	// data
	for _, data := range datablocks {
		rounded := b.blkshift.roundup(int64(len(data.data)))
		b.out.Write(data.data)
		pad(b.out, rounded-int64(len(data.data)))
	}

	return nil
}

func (db *dirbuilder) size(shift blkshift) int64 {
	const direntsize = 12

	sort.Slice(db.ents, func(i, j int) bool { return db.ents[i].name < db.ents[j].name })

	blocks := int64(0)
	remaining := shift.size()

	for _, ent := range db.ents {
		// need := int64(direntsize + len(ent.name) + 1)
		need := int64(direntsize + len(ent.name))
		if need > remaining {
			blocks++
			remaining = shift.size()
		}
		remaining -= need
	}

	return blocks<<shift + (shift.size() - remaining)
}

func (db *dirbuilder) write(out io.Writer, shift blkshift) {
	const direntsize = 12

	// already sorted in size(), don't need to sort again

	remaining := shift.size()
	ents := make([]erofs_dirent, 0, shift.size()/16)
	var names bytes.Buffer

	flush := func() {
		nameoff0 := uint16(len(ents) * direntsize)
		for i := range ents {
			ents[i].NameOff += nameoff0
		}
		pack(out, ents)
		io.Copy(out, &names)
		pad(out, remaining)

		ents = ents[:0]
		names.Reset()
		remaining = shift.size()
	}

	for _, ent := range db.ents {
		need := int64(direntsize + len(ent.name))
		if need > remaining {
			flush()
		}

		ents = append(ents, erofs_dirent{
			Nid:      uint64(ent.inum - 1),
			NameOff:  uint16(names.Len()), // offset minus nameoff0
			FileType: uint8(ent.tp),
		})
		names.Write([]byte(ent.name))
		remaining -= need
	}
	flush()

	return
}

var _zeros = make([]byte, 4096)

func pad(out io.Writer, n int64) {
	for {
		if n <= 4096 {
			out.Write(_zeros[:n])
			return
		}
		out.Write(_zeros)
		n -= 4096
	}
}

var _popts = struc.Options{Order: binary.LittleEndian}

func pack(out io.Writer, v any) error {
	return struc.PackWithOptions(out, v, &_popts)
}
