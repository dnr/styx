package erofs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"math"
	"path"
	"sort"

	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
	"github.com/klauspost/compress/zstd"
	"github.com/lunixbochs/struc"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"golang.org/x/sys/unix"
)

type (
	Builder struct {
		p builderParams
	}

	builderParams struct {
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

		taildataIsChunkIndex bool
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

func NewBuilder() *Builder {
	return &Builder{p: builderParams{
		blk: blkshift(12), // TODO: make configurable
	}}
}

func (b *Builder) BuildFromNar(r io.Reader, out io.Writer) error {
	narHasher := sha256.New()
	nr, err := nar.NewReader(io.TeeReader(r, narHasher))
	if err != nil {
		return err
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
		return tail > 0 && tail <= b.p.blk.size()-inodesize
	}
	setDataOnInode := func(i *inodebuilder, data []byte) {
		tail := b.p.blk.leftover(int64(len(data)))
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
		h, err := nr.Next()
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
				data, err := readFullFromNar(nr, h)
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
			if len(file) > EROFS_NAME_LEN {
				return errors.New("file name too long")
			}
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
	dummy := make([]byte, b.p.blk.size())
	for _, db := range dirs {
		db.sortAndSize(b.p.blk)
		tail := b.p.blk.leftover(db.size)
		if !allowedTail(tail) {
			tail = 0
		}
		// dummy data just to track size
		db.i.taildata = dummy[:tail]
	}

	// at this point:
	// all inodes have correct len(taildata)
	// files have correct taildata but dirs do not

	inodebase := max(4096, b.p.blk.size())

	// 2.2: lay out inodes and tails
	// using greedy for now. TODO: use best fit or something
	p := int64(0) // position relative to metadata area
	remaining := b.p.blk.size()
	for i, inode := range inodes {
		need := inodesize + inodeshift.roundup(int64(len(inode.taildata)))
		if need > remaining {
			p += remaining
			remaining = b.p.blk.size()
		}
		inodes[i].nid = truncU64(p >> inodeshift)
		p += need
		remaining -= need
	}
	if remaining < b.p.blk.size() {
		p += remaining
	}

	// at this point:
	// all inodes have correct nid

	// 2.3: write actual dirs to buffers
	for _, db := range dirs {
		var buf bytes.Buffer
		db.write(&buf, b.p.blk)
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
		data.i.i.IU = truncU32(p >> b.p.blk)
		p += b.p.blk.roundup(int64(len(data.data)))
	}
	log.Printf("final calculated size %d", p)

	// at this point:
	// all inodes have correct IU and ISize

	// pass 3: write

	// 3.1: fill in super
	super := erofs_super_block{
		Magic:       0xE0F5E1E2,
		BlkSzBits:   truncU8(b.p.blk),
		RootNid:     truncU16(root.nid),
		Inos:        truncU64(len(inodes)),
		Blocks:      truncU32(p >> b.p.blk),
		MetaBlkAddr: truncU32(inodebase >> b.p.blk),
	}
	narhash := narHasher.Sum(nil)
	copy(super.Uuid[:], narhash)
	copy(super.VolumeName[:], "styx-"+base64.RawURLEncoding.EncodeToString(narhash)[:10])

	c := crc32.NewIEEE()
	pack(c, &super)
	super.Checksum = c.Sum32()

	pad(out, EROFS_SUPER_OFFSET)
	pack(out, &super)
	pad(out, inodebase-EROFS_SUPER_OFFSET-128)

	// 3.2: inodes and tails
	remaining = b.p.blk.size()
	for _, i := range inodes {
		need := inodesize + inodeshift.roundup(int64(len(i.taildata)))
		if need > remaining {
			pad(out, remaining)
			remaining = b.p.blk.size()
		}
		pack(out, i.i)
		writeAndPad(out, i.taildata, inodeshift)
		remaining -= need
	}
	if remaining < b.p.blk.size() {
		pad(out, remaining)
	}

	// 3.4: full blocks
	for _, data := range datablocks {
		writeAndPad(out, data.data, b.p.blk)
	}

	return nil
}

// embed data in image
func (b *Builder) BuildFromManifestEmbed(
	ctx context.Context,
	r io.Reader,
	out io.Writer,
	cs manifester.ChunkStoreRead,
) error {
	// TODO: move manifest encoding to shared lib
	// TODO: verify signatures if requested
	var m *pb.Manifest
	if zr, err := zstd.NewReader(r); err != nil {
		return err
	} else if mbytes, err := io.ReadAll(zr); err != nil {
		return err
	} else if m, err = unmarshalAs[pb.Manifest](mbytes); err != nil {
		return err
	}

	hashBytes := int64(m.HashBits >> 3)

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
		return tail > 0 && tail <= b.p.blk.size()-inodesize
	}
	setDataOnInode := func(i *inodebuilder, data []byte) {
		tail := b.p.blk.leftover(int64(len(data)))
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

	// pass 1: read manifest

	for _, e := range m.Entries {
		// every entry gets an inode and all but the root get a dirent
		var fstype uint16
		i := &inodebuilder{
			i: erofs_inode_compact{
				IFormat: formatInline,
				IIno:    truncU32(len(inodes) + 37),
				INlink:  1,
			},
		}
		inodes = append(inodes, i)

		switch e.Type {
		case pb.EntryType_DIRECTORY:
			fstype = EROFS_FT_DIR
			i.i.IMode = unix.S_IFDIR | 0755
			i.i.INlink = 2
			parent := i
			if e.Path != "/" {
				parent = dirsmap[path.Dir(e.Path)].i
			}
			db := &dirbuilder{
				ents: []dbent{
					{name: ".", tp: EROFS_FT_DIR, i: i},
					{name: "..", tp: EROFS_FT_DIR, i: parent},
				},
				i: i,
			}
			dirs = append(dirs, db)
			dirsmap[e.Path] = db

		case pb.EntryType_REGULAR:
			fstype = EROFS_FT_REG_FILE
			i.i.IMode = unix.S_IFREG | 0644
			if e.Executable {
				i.i.IMode = unix.S_IFREG | 0755
			}
			if e.Size > 0 {
				var data []byte
				if len(e.TailData) > 0 {
					if int64(len(e.TailData)) != e.Size {
						return fmt.Errorf("tail data size mismatch")
					} else if !allowedTail(e.Size) {
						return fmt.Errorf("tail too big")
					}
					data = e.TailData
				} else if len(e.Digests) > 0 {
					if e.Size > math.MaxUint32 {
						return fmt.Errorf("TODO: support larger files with extended inode")
					}
					data = make([]byte, e.Size)
					for start := int64(0); start < e.Size; start += int64(m.ChunkSize) {
						idx := start / int64(m.ChunkSize)
						digest := e.Digests[idx*hashBytes : (idx+1)*hashBytes]
						digeststr := base64.RawURLEncoding.EncodeToString(digest)
						cdata, err := cs.Get(ctx, digeststr)
						if err != nil {
							return err
						}
						copy(data[start:], cdata)
					}
				} else {
					return fmt.Errorf("non-zero size but no taildata or digests")
				}
				setDataOnInode(i, data)
			}

		case pb.EntryType_SYMLINK:
			fstype = EROFS_FT_SYMLINK
			i.i.IMode = unix.S_IFLNK | 0777
			i.i.ISize = truncU32(len(e.TailData))
			i.taildata = e.TailData

		default:
			return errors.New("unknown type")
		}

		// add dirent to appropriate directory
		if e.Path == "/" {
			root = i
		} else {
			dir, file := path.Split(e.Path)
			if len(file) > EROFS_NAME_LEN {
				return errors.New("file name too long")
			}
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
	dummy := make([]byte, b.p.blk.size())
	for _, db := range dirs {
		db.sortAndSize(b.p.blk)
		tail := b.p.blk.leftover(db.size)
		if !allowedTail(tail) {
			tail = 0
		}
		// dummy data just to track size
		db.i.taildata = dummy[:tail]
	}

	// at this point:
	// all inodes have correct len(taildata)
	// files have correct taildata but dirs do not

	inodebase := max(4096, b.p.blk.size())

	// 2.2: lay out inodes and tails
	// using greedy for now. TODO: use best fit or something
	p := int64(0) // position relative to metadata area
	remaining := b.p.blk.size()
	for i, inode := range inodes {
		need := inodesize + inodeshift.roundup(int64(len(inode.taildata)))
		if need > remaining {
			p += remaining
			remaining = b.p.blk.size()
		}
		inodes[i].nid = truncU64(p >> inodeshift)
		p += need
		remaining -= need
	}
	if remaining < b.p.blk.size() {
		p += remaining
	}

	// at this point:
	// all inodes have correct nid

	// 2.3: write actual dirs to buffers
	for _, db := range dirs {
		var buf bytes.Buffer
		db.write(&buf, b.p.blk)
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
		data.i.i.IU = truncU32(p >> b.p.blk)
		p += b.p.blk.roundup(int64(len(data.data)))
	}
	log.Printf("final calculated size %d", p)

	// at this point:
	// all inodes have correct IU and ISize

	// pass 3: write

	// 3.1: fill in super
	super := erofs_super_block{
		Magic:       0xE0F5E1E2,
		BlkSzBits:   truncU8(b.p.blk),
		RootNid:     truncU16(root.nid),
		Inos:        truncU64(len(inodes)),
		Blocks:      truncU32(p >> b.p.blk),
		MetaBlkAddr: truncU32(inodebase >> b.p.blk),
	}

	var narhash []byte
	if len(m.Meta.GetNarinfo()) > 0 {
		ni, err := narinfo.Parse(bytes.NewReader(m.Meta.Narinfo))
		if err != nil {
			log.Println("couldn't parse narinfo in manifest", err)
		} else {
			narhash = ni.NarHash.Digest()
		}
	}
	if len(narhash) == 0 {
		narhash = make([]byte, 16)
		rand.Read(narhash)
	}
	copy(super.Uuid[:], narhash)
	copy(super.VolumeName[:], "styx-"+base64.RawURLEncoding.EncodeToString(narhash)[:10])

	c := crc32.NewIEEE()
	pack(c, &super)
	super.Checksum = c.Sum32()

	pad(out, EROFS_SUPER_OFFSET)
	pack(out, &super)
	pad(out, inodebase-EROFS_SUPER_OFFSET-128)

	// 3.2: inodes and tails
	remaining = b.p.blk.size()
	for _, i := range inodes {
		need := inodesize + inodeshift.roundup(int64(len(i.taildata)))
		if need > remaining {
			pad(out, remaining)
			remaining = b.p.blk.size()
		}
		pack(out, i.i)
		writeAndPad(out, i.taildata, inodeshift)
		remaining -= need
	}
	if remaining < b.p.blk.size() {
		pad(out, remaining)
	}

	// 3.4: full blocks
	for _, data := range datablocks {
		writeAndPad(out, data.data, b.p.blk)
	}

	return nil
}

// put data in slabs
func (b *Builder) BuildFromManifestWithSlab(
	ctx context.Context,
	r io.Reader,
	out io.Writer,
	sm SlabManager,
) error {
	// TODO: move manifest encoding to shared lib
	// TODO: verify signatures if requested
	var m *pb.Manifest
	if zr, err := zstd.NewReader(r); err != nil {
		return err
	} else if mbytes, err := io.ReadAll(zr); err != nil {
		return err
	} else if m, err = unmarshalAs[pb.Manifest](mbytes); err != nil {
		return err
	}

	hashBytes := int64(m.HashBits >> 3)
	if err := sm.VerifyParams(int(hashBytes), int(b.p.blk.size()), int(m.ChunkSize)); err != nil {
		return err
	}

	const inodeshift = blkshift(5)
	const inodesize = 1 << inodeshift
	const layoutCompact = EROFS_INODE_LAYOUT_COMPACT << EROFS_I_VERSION_BIT
	const formatPlain = (layoutCompact | (EROFS_INODE_FLAT_PLAIN << EROFS_I_DATALAYOUT_BIT))
	const formatInline = (layoutCompact | (EROFS_INODE_FLAT_INLINE << EROFS_I_DATALAYOUT_BIT))
	const formatChunked = (layoutCompact | (EROFS_INODE_CHUNK_BASED << EROFS_I_DATALAYOUT_BIT))

	chunkedIU, err := inodeChunkInfo(b.p.blk, 16) // FIXME: use m.ChunkSize here
	if err != nil {
		return err
	}

	var inodes []*inodebuilder
	var root *inodebuilder
	var datablocks []regulardata
	dirsmap := make(map[string]*dirbuilder)
	var dirs []*dirbuilder
	slabmap := make(map[uint16]uint16) // slab id -> device id

	allowedTail := func(tail int64) bool {
		// tail has to fit in a block with the inode
		return tail > 0 && tail <= b.p.blk.size()-inodesize
	}
	setDataOnInode := func(i *inodebuilder, data []byte) {
		tail := b.p.blk.leftover(int64(len(data)))
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

	// pass 1: read manifest

	for _, e := range m.Entries {
		// every entry gets an inode and all but the root get a dirent
		var fstype uint16
		i := &inodebuilder{
			i: erofs_inode_compact{
				IFormat: formatInline,
				IIno:    truncU32(len(inodes) + 37),
				INlink:  1,
			},
		}
		inodes = append(inodes, i)

		switch e.Type {
		case pb.EntryType_DIRECTORY:
			fstype = EROFS_FT_DIR
			i.i.IMode = unix.S_IFDIR | 0755
			i.i.INlink = 2
			parent := i
			if e.Path != "/" {
				parent = dirsmap[path.Dir(e.Path)].i
			}
			db := &dirbuilder{
				ents: []dbent{
					{name: ".", tp: EROFS_FT_DIR, i: i},
					{name: "..", tp: EROFS_FT_DIR, i: parent},
				},
				i: i,
			}
			dirs = append(dirs, db)
			dirsmap[e.Path] = db

		case pb.EntryType_REGULAR:
			fstype = EROFS_FT_REG_FILE
			i.i.IMode = unix.S_IFREG | 0644
			if e.Executable {
				i.i.IMode = unix.S_IFREG | 0755
			}
			if e.Size > 0 {
				var data []byte
				if len(e.TailData) > 0 {
					if int64(len(e.TailData)) != e.Size {
						return fmt.Errorf("tail data size mismatch")
					} else if !allowedTail(e.Size) {
						return fmt.Errorf("tail too big")
					}
					data = e.TailData
					setDataOnInode(i, data)
				} else if len(e.Digests) > 0 {
					if e.Size > math.MaxUint32 {
						return fmt.Errorf("TODO: support larger files with extended inode")
					}
					// TODO: get chunk size in blkshift and use roundup
					nChunks := (e.Size + int64(m.ChunkSize) - 1) / int64(m.ChunkSize)
					if int64(len(e.Digests)) != nChunks*hashBytes {
						return fmt.Errorf("digest list wrong size")
					}
					blocks := make([]uint16, nChunks)
					allButLast := truncU16(m.ChunkSize >> b.p.blk)
					for j := range blocks {
						blocks[j] = allButLast
					}
					// TODO: write this better
					lastChunkLen := e.Size & int64(m.ChunkSize-1)
					blocks[len(blocks)-1] = truncU16(b.p.blk.roundup(lastChunkLen) >> b.p.blk)
					// TODO: do in larger batches
					locs, err := sm.AllocateBatch(blocks, e.Digests)
					if err != nil {
						return err
					}
					i.i.IFormat = formatChunked
					i.i.IU = chunkedIU
					idxs := make([]erofs_inode_chunk_index, nChunks)
					for i, loc := range locs {
						devId, ok := slabmap[loc.SlabId]
						if !ok {
							devId = truncU16(len(slabmap))
							slabmap[loc.SlabId] = devId
						}
						idxs[i].DeviceId = devId
						idxs[i].BlkAddr = loc.Addr
					}
					if i.taildata, err = packToBytes(idxs); err != nil {
						return err
					}
					i.taildataIsChunkIndex = true
				} else {
					return fmt.Errorf("non-zero size but no taildata or digests")
				}
			}

		case pb.EntryType_SYMLINK:
			fstype = EROFS_FT_SYMLINK
			i.i.IMode = unix.S_IFLNK | 0777
			i.i.ISize = truncU32(len(e.TailData))
			i.taildata = e.TailData

		default:
			return errors.New("unknown type")
		}

		// add dirent to appropriate directory
		if e.Path == "/" {
			root = i
		} else {
			dir, file := path.Split(e.Path)
			if len(file) > EROFS_NAME_LEN {
				return errors.New("file name too long")
			}
			db := dirsmap[path.Clean(dir)]
			db.ents = append(db.ents, dbent{
				name: file,
				i:    i,
				tp:   fstype,
			})
		}
	}

	// pass 2: pack inodes and tails

	// 2.0: device table
	devs := make([]erofs_deviceslot, len(slabmap))
	for slabId, devId := range slabmap {
		tag, blocks := sm.SlabInfo(slabId)
		devs[devId].Blocks = blocks
		copy(devs[devId].Tag[:], tag)
	}
	devTableSize := int64(len(devs) * 128)

	// 2.1: directory sizes
	dummy := make([]byte, b.p.blk.size())
	for _, db := range dirs {
		db.sortAndSize(b.p.blk)
		tail := b.p.blk.leftover(db.size)
		if !allowedTail(tail) {
			tail = 0
		}
		// dummy data just to track size
		db.i.taildata = dummy[:tail]
	}

	// at this point:
	// all inodes have correct len(taildata)
	// files have correct taildata but dirs do not

	// TODO: test this math with > 23 devs
	inodebase := b.p.blk.roundup(EROFS_SUPER_OFFSET + 128 + devTableSize)

	// 2.2: lay out inodes and tails
	// using greedy for now. TODO: use best fit or something
	p := int64(0) // position relative to inode base ("metadata area")
	blkoff := int64(0)
	for i, inode := range inodes {
		need := inodesize + inodeshift.roundup(int64(len(inode.taildata)))

		if inode.taildataIsChunkIndex {
			// chunk index does not need to fit in a block, it just gets laid out directly
			inodes[i].nid = truncU64(p >> inodeshift)
			p += need
			blkoff = b.p.blk.leftover(blkoff + need)
			continue
		}

		if blkoff+need > b.p.blk.size() {
			p += b.p.blk.size() - blkoff
			blkoff = 0
		}
		inodes[i].nid = truncU64(p >> inodeshift)
		p += need
		blkoff = b.p.blk.leftover(blkoff + need)
	}
	if blkoff > 0 {
		p += b.p.blk.size() - blkoff
	}

	// at this point:
	// all inodes have correct nid

	// 2.3: write actual dirs to buffers
	for _, db := range dirs {
		var buf bytes.Buffer
		db.write(&buf, b.p.blk)
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
		data.i.i.IU = truncU32(p >> b.p.blk)
		p += b.p.blk.roundup(int64(len(data.data)))
	}
	log.Printf("final calculated size %d", p)

	// at this point:
	// all inodes have correct IU and ISize

	// pass 3: write

	// 3.1: fill in super
	const incompat = (EROFS_FEATURE_INCOMPAT_CHUNKED_FILE |
		EROFS_FEATURE_INCOMPAT_DEVICE_TABLE)

	super := erofs_super_block{
		Magic:           0xE0F5E1E2,
		FeatureIncompat: incompat,
		BlkSzBits:       truncU8(b.p.blk),
		RootNid:         truncU16(root.nid),
		Inos:            truncU64(len(inodes)),
		Blocks:          truncU32(p >> b.p.blk),
		MetaBlkAddr:     truncU32(inodebase >> b.p.blk),
		ExtraDevices:    truncU16(len(devs)),
		DevtSlotOff:     (EROFS_SUPER_OFFSET + 128) / 128, // TODO: use constants
	}

	var narhash []byte
	if len(m.Meta.GetNarinfo()) > 0 {
		ni, err := narinfo.Parse(bytes.NewReader(m.Meta.Narinfo))
		if err != nil {
			log.Println("couldn't parse narinfo in manifest", err)
		} else {
			narhash = ni.NarHash.Digest()
		}
	}
	if len(narhash) == 0 {
		narhash = make([]byte, 16)
		rand.Read(narhash)
	}
	copy(super.Uuid[:], narhash)
	copy(super.VolumeName[:], "styx-"+base64.RawURLEncoding.EncodeToString(narhash)[:10])

	c := crc32.NewIEEE()
	pack(c, &super)
	super.Checksum = c.Sum32()

	pad(out, EROFS_SUPER_OFFSET)
	pack(out, &super)
	pack(out, devs)
	pad(out, inodebase-EROFS_SUPER_OFFSET-128-devTableSize)

	// 3.2: inodes and tails
	blkoff = 0
	for _, i := range inodes {
		need := inodesize + inodeshift.roundup(int64(len(i.taildata)))

		if i.taildataIsChunkIndex {
			// chunk index does not need to fit in a block, it just gets laid out directly
			pack(out, i.i)
			writeAndPad(out, i.taildata, inodeshift)
			blkoff = b.p.blk.leftover(blkoff + need)
			continue
		}

		if blkoff+need > b.p.blk.size() {
			pad(out, b.p.blk.size()-blkoff)
			blkoff = 0
		}
		pack(out, i.i)
		writeAndPad(out, i.taildata, inodeshift)
		blkoff = b.p.blk.leftover(blkoff + need)
	}
	if blkoff > 0 {
		pad(out, b.p.blk.size()-blkoff)
	}

	// 3.4: full blocks
	for _, data := range datablocks {
		writeAndPad(out, data.data, b.p.blk)
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

func packToBytes(v any) ([]byte, error) {
	var b bytes.Buffer
	err := struc.PackWithOptions(&b, v, &_popts)
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeAndPad(out io.Writer, data []byte, shift blkshift) error {
	n, err := out.Write(data)
	r := shift.roundup(int64(n)) - int64(n)
	pad(out, r)
	return err
}

func inodeChunkInfo(blkbits, chunkbits blkshift) (uint32, error) {
	if chunkbits-blkbits > 31 {
		return 0, fmt.Errorf("chunk size too big")
	}
	b, err := packToBytes(erofs_inode_chunk_info{
		Format: EROFS_CHUNK_FORMAT_INDEXES | uint16(chunkbits-blkbits),
	})
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
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
