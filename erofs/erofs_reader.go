package erofs

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/lunixbochs/struc"
	"golang.org/x/sys/unix"

	"github.com/dnr/styx/pb"
)

func ReadErofs(img []byte, expectedBlkShift int) (out []*pb.Entry, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = r.(error)
		}
	}()
	var super erofs_super_block
	if err := unpackBytes(img[EROFS_SUPER_OFFSET:EROFS_SUPER_OFFSET+128], &super); err != nil {
		return nil, err
	} else if super.Magic != EROFS_MAGIC {
		return nil, errors.New("bad magic")
	} else if int(super.BlkSzBits) != expectedBlkShift {
		return nil, errors.New("block size mismatch")
	}

	const inodeshift = blkshift(5)
	const inodesize = 1 << inodeshift
	blkshift := blkshift(expectedBlkShift)
	metaBase := uint64(super.MetaBlkAddr) << blkshift

	// FIXME: is this right or are there more?
	out = make([]*pb.Entry, 0, super.Inos)

	var readdir func(path string, data []byte)

	readnid := func(path string, nid uint64) {
		p := metaBase + nid<<inodeshift
		var i erofs_inode_compact
		if err := unpackBytes(img[p:p+32], &i); err != nil {
			panic(err)
		} else if (i.IFormat>>EROFS_I_VERSION_BIT)&EROFS_I_VERSION_MASK != EROFS_INODE_COMPRESSED_COMPACT {
			panic(errors.New("can't handle non-compact inodes"))
		}
		layout := (i.IFormat >> EROFS_I_DATALAYOUT_BIT) & EROFS_I_DATALAYOUT_MASK

		ent := &pb.Entry{
			Path: path,
			Size: i.ISize,
		}
		out = append(out, ent)

		// read data
		var data, chunkIndexes []byte
		switch layout {
		case EROFS_INODE_FLAT_PLAIN:
			// FIXME
		case EROFS_INODE_FLAT_INLINE:
			data = img[p+32 : p+32+i.ISize]
		case EROFS_INODE_CHUNK_BASED:
			// FIXME: not digests, chunk addrs. maybe return in this field anyway?
			chunkIndexes = img[p+32 : p+32+nChunks*8]
		default:
			panic(fmt.Errorf("bad layout %v", layout))
		}

		// set type
		switch i.IMode & unix.S_IFMT {
		case unix.S_IFREG:
			ent.Type = pb.EntryType_REGULAR
			ent.Executable = i.IMode&0o111 != 0
			if data != nil {
				ent.InlineData = data
			} else if chunkIndexes != nil {
				// FIXME: chunk indexes aren't digests
				ent.Digests = chunkIndexes
			}
		case unix.S_IFDIR:
			ent.Type = pb.EntryType_DIRECTORY
			if data == nil {
				panic(fmt.Errorf("directory missing data"))
			}
			readdir(path, data)
		case unix.S_IFLNK:
			ent.Type = pb.EntryType_SYMLINK
			if data == nil {
				panic(fmt.Errorf("symlink missing data"))
			}
			ent.InlineData = data
		default:
			panic(fmt.Errorf("bad mode %v", i.IMode))
		}
	}

	readdir = func(path string, data []byte) {
		// FIXME
	}

	readnid("/", uint64(super.RootNid))
	return
}

func unpack(in io.Reader, v any) error {
	return struc.UnpackWithOptions(in, v, &_popts)
}

func unpackBytes(b []byte, v any) error {
	return struc.UnpackWithOptions(bytes.NewReader(b), v, &_popts)
}
