package styx

import (
	"errors"
	"fmt"
	"io"
	"math"
	"path"

	"github.com/nix-community/go-nix/pkg/nar"
	"golang.org/x/sys/unix"
)

type (
	builder struct {
		nar *nar.Reader
		out io.WriterAt

		blkshift blkshift
	}

	regulardata struct {
		blockaddr int64 // FIXME: needed?
		inum      uint32
		data      []byte
	}

	dbent struct {
		name string
		inum uint32
		tp   uint16
	}

	dirbuilder struct {
		blockaddr int64 // FIXME: needed?
		ents      []dbent
		inum      uint32
	}
)

func newBuilder(nar *nar.Reader, out io.WriterAt) *builder {
	return &builder{nar: nar, out: out, blkshift: blkshift(12)}
}

func (b *builder) build() error {
	var inum, root uint32
	var inodes []*erofs_inode_compact // note inodes[0].inum = 1
	var datablocks []regulardata
	var dirsmap map[string]*dirbuilder
	var dirs []*dirbuilder

	for {
		h, err := b.nar.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		} else if err = h.Validate(); err != nil {
			return err
		}

		// every header gets an inode and all but the root get a dirent
		var deType uint16
		inum++
		i := &erofs_inode_compact{
			IFormat: ((EROFS_INODE_LAYOUT_COMPACT << EROFS_I_VERSION_BIT) |
				(EROFS_INODE_FLAT_PLAIN << EROFS_I_DATALAYOUT_BIT)),
			IIno:   inum,
			INlink: 1,
		}
		inodes = append(inodes, i)

		switch h.Type {
		case nar.TypeDirectory:
			deType = unix.DT_DIR
			i.IMode = unix.S_IFDIR | 0755
			// i.ISize = FIXME
			parentInum := dirsmap[path.Dir(h.Path)].inum
			db := &dirbuilder{
				ents: []dbent{
					{name: ".", tp: unix.DT_DIR, inum: inum},
					{name: "..", tp: unix.DT_DIR, inum: parentInum},
				},
				inum: inum,
			}
			dirs = append(dirs, db)
			dirsmap[h.Path] = db
		case nar.TypeRegular:
			deType = unix.DT_REG
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
			deType = unix.DT_LNK
			i.IMode = unix.S_IFLNK | 0777
			// FIXME: contents!! tail pack?
		default:
			return errors.New("unknown type")
		}

		// add dirent to appropriate directory
		if h.Path == "/" {
			if deType != unix.DT_DIR {
				return errors.New("can't handle bare file nars yet")
			}
			root = inum
		} else {
			dir, file := path.Split(h.Path)
			db := dirsmap[dir]
			db.ents = append(db.ents, dbent{
				name: file,
				inum: inum,
				tp:   deType,
			})
		}
	}

	// layout: inodes start at 4096 or first block
	// all inodes in a row, then all dirs, then all regular files

	// pass 1: calculate block offsets
	inodebase := max(4096, b.blkshift.size())
	inodelen := b.blkshift.roundup(int64(len(inodes)) * 32)
	dirbase := inodebase + inodelen
	p := dirbase
	for _, db := range dirs {
		db.blockaddr = p >> b.blkshift
		dblen := db.length()
		p += dblen
		inodes[db.inum-1].IU = uint32(db.blockaddr) // FIXME: check overflow
	}
	for _, data := range datablocks {
		data.blockaddr = p >> b.blkshift
		inodes[data.inum-1].IU = uint32(data.blockaddr) // FIXME: check overflow
		p += b.blkshift.roundup(int64(len(data.data)))
	}

	// pass 2: write
	_ = root

	return nil
}

func (db *dirbuilder) length() int64 {
	return 0
}
