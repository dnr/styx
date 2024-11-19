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
	"golang.org/x/exp/slices"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/pb"
)

const (
	BarePath = "/___bare___"
)

// larger block sizes don't seem to work with erofs yet
const (
	maxBlockShift = 12
	maxBlockSize  = 1 << maxBlockShift
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

		batchStart, batchEnd int
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

func IsBare(fs []byte) bool {
	// See comment in BuildFromManifestWithSlab.
	const volIdOffset = 64
	return fs[EROFS_SUPER_OFFSET+volIdOffset+3]&32 == 0
}

func NewBuilder(cfg BuilderConfig) *Builder {
	if cfg.BlockShift > maxBlockShift {
		panic("larger block size not supported yet")
	}
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
	if err := sm.VerifyParams(b.blk); err != nil {
		return err
	}

	if len(m.Entries) == 0 {
		return errors.New("no entries")
	}

	isBare := false
	if e0 := m.Entries[0]; e0.Type != pb.EntryType_DIRECTORY {
		if len(m.Entries) != 1 {
			return errors.New("bare file must be only entry")
		} else if e0.Type != pb.EntryType_REGULAR {
			return errors.New("bare file can't be symlink")
		}
		// this is a bare file. fake a directory for it
		isBare = true
		e1 := proto.Clone(e0).(*pb.Entry)
		e1.Path = BarePath
		m = &pb.Manifest{
			Params: m.Params,
			Entries: []*pb.Entry{
				&pb.Entry{
					Path: "/",
					Type: pb.EntryType_DIRECTORY,
				},
				e1,
			},
			SmallFileCutoff: m.SmallFileCutoff,
			Meta:            m.Meta,
		}
	}

	const layoutCompact = EROFS_INODE_LAYOUT_COMPACT << EROFS_I_VERSION_BIT
	const formatPlain = (layoutCompact | (EROFS_INODE_FLAT_PLAIN << EROFS_I_DATALAYOUT_BIT))
	const formatInline = (layoutCompact | (EROFS_INODE_FLAT_INLINE << EROFS_I_DATALAYOUT_BIT))
	const formatChunked = (layoutCompact | (EROFS_INODE_CHUNK_BASED << EROFS_I_DATALAYOUT_BIT))

	var inodes []*inodebuilder
	var root *inodebuilder
	var datablocks []regulardata
	dirsmap := make(map[string]*dirbuilder)
	var dirs []*dirbuilder
	slabmap := make(map[uint16]uint16) // slab id -> device id

	allowedTail := func(tail int64) bool {
		// tail has to fit in a block with the inode
		return tail > 0 && tail <= b.blk.Size()-EROFS_COMPACT_INODE_SIZE
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

	const batchSize = 1000
	batchBlocks := make([]uint16, 0, batchSize)
	batchDigests := make([]cdig.CDig, 0, batchSize)
	batchInodes := make([]*inodebuilder, 0, batchSize/10)

	flushBlocks := func() error {
		allLocs, err := sm.AllocateBatch(ctx, batchBlocks, batchDigests)
		if err != nil {
			return err
		}

		for _, i := range batchInodes {
			locs := allLocs[i.batchStart:i.batchEnd]
			idxs := make([]erofs_inode_chunk_index, len(locs))
			for j, loc := range locs {
				devId, ok := slabmap[loc.SlabId]
				if !ok {
					devId = common.TruncU16(len(slabmap))
					slabmap[loc.SlabId] = devId
				}
				idxs[j].DeviceId = devId + 1
				idxs[j].BlkAddr = loc.Addr
			}
			if i.taildata, err = packToBytes(idxs); err != nil {
				return err
			}
			i.taildataIsChunkIndex = true
		}

		batchBlocks = batchBlocks[:0]
		batchDigests = batchDigests[:0]
		batchInodes = batchInodes[:0]
		return nil
	}

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
				pdir := dirsmap[path.Dir(e.Path)]
				if pdir == nil {
					return errors.New("found entry before parent dir")
				}
				parent = pdir.i
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
			i.i.IMode = unix.S_IFREG | uint16(e.FileMode())
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
				i.i.IFormat = formatChunked
				cshift := common.BlkShift(e.ChunkShiftDef())
				chunkedIU, err := inodeChunkInfo(b.blk, cshift)
				if err != nil {
					return err
				}
				i.i.IU = chunkedIU

				nChunks := int(cshift.Blocks(e.Size))
				if len(e.Digests) != nChunks*cdig.Bytes {
					return fmt.Errorf("digest list wrong size")
				}
				if len(batchBlocks)+nChunks > batchSize {
					if err := flushBlocks(); err != nil {
						return err
					}
				}
				i.batchStart = len(batchBlocks)
				batchBlocks = common.AppendBlocksList(batchBlocks, e.Size, b.blk)
				i.batchEnd = len(batchBlocks)
				batchDigests = append(batchDigests, cdig.FromSliceAlias(e.Digests)...)
				batchInodes = append(batchInodes, i)
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
	if err := flushBlocks(); err != nil {
		return err
	}

	// pass 2: pack inodes and tails

	// 2.0: device table
	devs := make([]erofs_deviceslot, len(slabmap))
	for slabId, devId := range slabmap {
		tag, blocks := sm.SlabInfo(slabId)
		devs[devId].Blocks = blocks
		copy(devs[devId].Tag[:], tag)
	}
	devTableSize := int64(len(devs) * EROFS_DEVT_SLOT_SIZE)
	superEnd := EROFS_SUPER_OFFSET + EROFS_SUPER_SIZE + devTableSize

	// 2.1: directory sizes
	for _, db := range dirs {
		db.sortAndSize(b.blk)
		tail := b.blk.Leftover(db.size)
		if !allowedTail(tail) {
			tail = 0
		}
		// dummy data just to track size
		db.i.taildata = _zeros[:tail]
	}

	// at this point:
	// all inodes have correct len(taildata)
	// files have correct taildata but dirs do not

	// 2.2: lay out inodes and tails
	// use limited first-fit
	type inodespace struct {
		addr uint32
		off  uint16
		ln   uint16
	}
	// available space in first block:
	inodealloc := []inodespace{
		inodespace{0, 0, EROFS_SUPER_OFFSET},
		inodespace{0, common.TruncU16(superEnd), common.TruncU16(b.blk.Size() - superEnd)},
	}
	nextAddr := uint32(1)

	for _, inode := range inodes {
		need := EROFS_COMPACT_INODE_SIZE + EROFS_NID_SHIFT.Roundup(int64(len(inode.taildata)))
		found := -1
		for idx, space := range inodealloc {
			if int64(space.ln) >= need {
				found = idx
				break
			}
		}
		if found == -1 {
			if inode.taildataIsChunkIndex &&
				len(inodealloc) > 0 &&
				inodealloc[len(inodealloc)-1].addr == nextAddr-1 &&
				// this check is that we don't "overflow" the pre-super space in the first block
				(inodealloc[len(inodealloc)-1].addr > 0 || inodealloc[len(inodealloc)-1].off > EROFS_SUPER_OFFSET) {
				// can use last block and overflow
				found = len(inodealloc) - 1
			} else {
				// allocate new block
				found = len(inodealloc)
				inodealloc = append(inodealloc, inodespace{nextAddr, 0, common.TruncU16(b.blk.Size())})
				nextAddr++
			}
		}

		space := inodealloc[found]
		inode.nid = (uint64(space.addr)<<b.blk + uint64(space.off)) >> EROFS_NID_SHIFT
		if inode.taildataIsChunkIndex && need > int64(space.ln) {
			// overflow this one (must be last)
			if found != len(inodealloc)-1 || space.addr != nextAddr-1 {
				panic("bug")
			}
			need -= int64(space.ln)
			space.addr += 1 + common.TruncU32(need>>b.blk)
			nextAddr = space.addr + 1
			space.off = common.TruncU16(b.blk.Leftover(need))
			space.ln = common.TruncU16(b.blk.Size()) - space.off
		} else {
			space.off += common.TruncU16(need)
			space.ln -= common.TruncU16(need)
		}
		inodealloc[found] = space
		// clear filled blocks and also anything more than 16 back
		inodealloc = slices.DeleteFunc(inodealloc, func(space inodespace) bool {
			return space.ln == 0 || nextAddr-space.addr > 16
		})
	}

	// at this point:
	// all inodes have correct nid

	// sort inodes by nid so we can write them in the right order
	slices.SortFunc(inodes, func(a, b *inodebuilder) int {
		return int(a.nid) - int(b.nid)
	})

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

	// start data blocks at block following last inode block
	// (if last inode was chunk data and exactly filled an overflow block, this will leave one
	// unused block after the inodes. just don't care about that case.)
	dataStart := int64(nextAddr) << b.blk
	p := dataStart

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

	// write pre-super inodes
	p = 0
	writeInode := func(i *inodebuilder) {
		needPad := int64(i.nid)<<EROFS_NID_SHIFT - p
		pad(out, needPad)
		p += needPad
		pack(out, i.i)
		p += EROFS_COMPACT_INODE_SIZE
		if i.taildata != nil {
			p += writeAndPad(out, i.taildata, EROFS_NID_SHIFT)
		}
	}
	for _, i := range inodes {
		if i.nid >= EROFS_SUPER_OFFSET>>EROFS_NID_SHIFT {
			break
		}
		writeInode(i)
	}

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
		MetaBlkAddr:     common.TruncU32(0),
		ExtraDevices:    common.TruncU16(len(devs)),
		DevtSlotOff:     (EROFS_SUPER_OFFSET + EROFS_SUPER_SIZE) / EROFS_DEVT_SLOT_SIZE,
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
	if isBare {
		// This is gross but okay for now: use one fixed bit to indicate bare. See IsBare.
		super.VolumeName[3] = 'X'
	}

	c := crc32.NewIEEE()
	pack(c, &super)
	super.Checksum = c.Sum32()

	pad(out, EROFS_SUPER_OFFSET-p)
	pack(out, &super)
	pack(out, devs)
	p = superEnd

	// 3.2: inodes and tails
	for _, i := range inodes {
		if i.nid < EROFS_SUPER_OFFSET>>EROFS_NID_SHIFT {
			continue
		}
		writeInode(i)
	}
	pad(out, dataStart-p)

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

var _zeros = make([]byte, maxBlockSize)

func pad(out io.Writer, n int64) error {
	for {
		if n == 0 {
			return nil
		} else if n <= maxBlockSize {
			_, err := out.Write(_zeros[:n])
			return err
		}
		out.Write(_zeros)
		n -= maxBlockSize
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

func writeAndPad(out io.Writer, data []byte, shift common.BlkShift) int64 {
	n, err := out.Write(data)
	if err != nil || n != len(data) {
		panic("write err")
	}
	rounded := shift.Roundup(int64(n))
	pad(out, rounded-int64(n))
	return rounded
}

func inodeChunkInfo(bshift, cshift common.BlkShift) (uint32, error) {
	if cshift-bshift > EROFS_CHUNK_FORMAT_BLKBITS_MASK {
		return 0, fmt.Errorf("chunk size too big")
	}
	b, err := packToBytes(erofs_inode_chunk_info{
		Format: EROFS_CHUNK_FORMAT_INDEXES | uint16(cshift-bshift),
	})
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}
