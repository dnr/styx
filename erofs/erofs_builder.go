package erofs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"path"
	"sort"

	"github.com/lunixbochs/struc"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/nar"
	"golang.org/x/sys/unix"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/pb"
)

type (
	BuilderConfig struct {
		BlockShift int
	}

	Builder struct {
		blk common.BlkShift
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

func NewBuilder(cfg BuilderConfig) *Builder {
	return &Builder{
		blk: common.BlkShift(cfg.BlockShift),
	}
}

func (b *Builder) BuildFromManifestWithSlab(
	ctx context.Context,
	m *pb.Manifest,
	out io.Writer,
	sm SlabManager,
) error {
	hashBytes := int(m.Params.DigestBits >> 3)
	chunkShift := common.BlkShift(m.Params.ChunkShift)

	if err := sm.VerifyParams(hashBytes, int(b.blk.Size()), int(chunkShift.Size())); err != nil {
		return err
	}

	const inodeshift = common.BlkShift(5)
	const inodesize = 1 << inodeshift
	const layoutCompact = EROFS_INODE_LAYOUT_COMPACT << EROFS_I_VERSION_BIT
	const formatPlain = (layoutCompact | (EROFS_INODE_FLAT_PLAIN << EROFS_I_DATALAYOUT_BIT))
	const formatInline = (layoutCompact | (EROFS_INODE_FLAT_INLINE << EROFS_I_DATALAYOUT_BIT))
	const formatChunked = (layoutCompact | (EROFS_INODE_CHUNK_BASED << EROFS_I_DATALAYOUT_BIT))

	chunkedIU, err := inodeChunkInfo(b.blk, chunkShift)
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
		return tail > 0 && tail <= b.blk.Size()-inodesize
	}
	setDataOnInode := func(i *inodebuilder, data []byte) {
		tail := b.blk.Leftover(int64(len(data)))
		i.i.ISize = common.TruncU32(len(data))
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
				IIno:    common.TruncU32(len(inodes) + 37),
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
			if len(e.InlineData) > 0 {
				if int64(len(e.InlineData)) != e.Size {
					return fmt.Errorf("tail data size mismatch")
				} else if !allowedTail(e.Size) {
					return fmt.Errorf("tail too big")
				}
				setDataOnInode(i, e.InlineData)
			} else if len(e.Digests) > 0 {
				if e.Size > math.MaxUint32 {
					return fmt.Errorf("TODO: support larger files with extended inode")
				}
				i.i.ISize = common.TruncU32(e.Size)
				blocks := common.MakeBlocksList(e.Size, chunkShift, b.blk)
				if len(e.Digests) != len(blocks)*hashBytes {
					return fmt.Errorf("digest list wrong size")
				}
				// TODO: do in larger batches
				locs, err := sm.AllocateBatch(ctx, blocks, e.Digests)
				if err != nil {
					return err
				}
				i.i.IFormat = formatChunked
				i.i.IU = chunkedIU
				idxs := make([]erofs_inode_chunk_index, len(blocks))
				for i, loc := range locs {
					devId, ok := slabmap[loc.SlabId]
					if !ok {
						devId = common.TruncU16(len(slabmap))
						slabmap[loc.SlabId] = devId
					}
					idxs[i].DeviceId = devId + 1
					idxs[i].BlkAddr = loc.Addr
				}
				if i.taildata, err = packToBytes(idxs); err != nil {
					return err
				}
				i.taildataIsChunkIndex = true
			} else if e.Size > 0 {
				return fmt.Errorf("non-zero size but no taildata or digests")
			}

		case pb.EntryType_SYMLINK:
			fstype = EROFS_FT_SYMLINK
			i.i.IMode = unix.S_IFLNK | 0777
			i.i.ISize = common.TruncU32(len(e.InlineData))
			i.taildata = e.InlineData

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
	dummy := make([]byte, b.blk.Size())
	for _, db := range dirs {
		db.sortAndSize(b.blk)
		tail := b.blk.Leftover(db.size)
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
	inodebase := b.blk.Roundup(EROFS_SUPER_OFFSET + 128 + devTableSize)

	// 2.2: lay out inodes and tails
	// using greedy for now. TODO: use best fit or something
	p := int64(0) // position relative to inode base ("metadata area")
	blkoff := int64(0)
	for i, inode := range inodes {
		need := inodesize + inodeshift.Roundup(int64(len(inode.taildata)))

		if inode.taildataIsChunkIndex {
			// chunk index does not need to fit in a block, it just gets laid out directly
			inodes[i].nid = common.TruncU64(p >> inodeshift)
			p += need
			blkoff = b.blk.Leftover(blkoff + need)
			continue
		}

		if blkoff+need > b.blk.Size() {
			p += b.blk.Size() - blkoff
			blkoff = 0
		}
		inodes[i].nid = common.TruncU64(p >> inodeshift)
		p += need
		blkoff = b.blk.Leftover(blkoff + need)
	}
	if blkoff > 0 {
		p += b.blk.Size() - blkoff
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
		data.i.i.IU = common.TruncU32(p >> b.blk)
		p += b.blk.Roundup(int64(len(data.data)))
	}

	// this is the end
	finalImageSize := p

	// at this point:
	// all inodes have correct IU and ISize

	// pass 3: write

	// 3.1: fill in super
	const incompat = (EROFS_FEATURE_INCOMPAT_CHUNKED_FILE |
		EROFS_FEATURE_INCOMPAT_DEVICE_TABLE)

	super := erofs_super_block{
		Magic:           EROFS_MAGIC,
		FeatureIncompat: incompat,
		BlkSzBits:       common.TruncU8(b.blk),
		RootNid:         common.TruncU16(root.nid),
		Inos:            common.TruncU64(len(inodes)),
		Blocks:          common.TruncU32(finalImageSize >> b.blk),
		MetaBlkAddr:     common.TruncU32(inodebase >> b.blk),
		ExtraDevices:    common.TruncU16(len(devs)),
		DevtSlotOff:     (EROFS_SUPER_OFFSET + 128) / 128, // TODO: use constants
	}

	var narhash []byte
	if h, err := hash.ParseNixBase32(m.Meta.GetNarinfo().GetNarHash()); err == nil {
		narhash = h.Digest()
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
		need := inodesize + inodeshift.Roundup(int64(len(i.taildata)))

		if i.taildataIsChunkIndex {
			// chunk index does not need to fit in a block, it just gets laid out directly
			pack(out, i.i)
			writeAndPad(out, i.taildata, inodeshift)
			blkoff = b.blk.Leftover(blkoff + need)
			continue
		}

		if blkoff+need > b.blk.Size() {
			pad(out, b.blk.Size()-blkoff)
			blkoff = 0
		}
		pack(out, i.i)
		writeAndPad(out, i.taildata, inodeshift)
		blkoff = b.blk.Leftover(blkoff + need)
	}
	if blkoff > 0 {
		pad(out, b.blk.Size()-blkoff)
	}

	// 3.4: full blocks
	for _, data := range datablocks {
		writeAndPad(out, data.data, b.blk)
	}

	return nil
}

func (db *dirbuilder) sortAndSize(shift common.BlkShift) {
	const direntsize = 12

	sort.Slice(db.ents, func(i, j int) bool { return db.ents[i].name < db.ents[j].name })

	blocks := int64(0)
	remaining := shift.Size()

	for _, ent := range db.ents {
		need := int64(direntsize + len(ent.name))
		if need > remaining {
			blocks++
			remaining = shift.Size()
		}
		remaining -= need
	}

	db.size = blocks<<shift + (shift.Size() - remaining)
}

func (db *dirbuilder) write(out io.Writer, shift common.BlkShift) {
	const direntsize = 12

	remaining := shift.Size()
	ents := make([]erofs_dirent, 0, shift.Size()/16)
	var names bytes.Buffer

	flush := func(isTail bool) {
		nameoff0 := common.TruncU16(len(ents) * direntsize)
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
		remaining = shift.Size()
	}

	for _, ent := range db.ents {
		need := int64(direntsize + len(ent.name))
		if need > remaining {
			flush(false)
		}

		ents = append(ents, erofs_dirent{
			Nid:      ent.i.nid,
			NameOff:  common.TruncU16(names.Len()), // offset minus nameoff0
			FileType: common.TruncU8(ent.tp),
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
	return common.ValOrErr(b.Bytes(), err)
}

func writeAndPad(out io.Writer, data []byte, shift common.BlkShift) error {
	n, err := out.Write(data)
	r := shift.Roundup(int64(n)) - int64(n)
	pad(out, r)
	return err
}

func inodeChunkInfo(blkbits, chunkbits common.BlkShift) (uint32, error) {
	if chunkbits-blkbits > EROFS_CHUNK_FORMAT_BLKBITS_MASK {
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
