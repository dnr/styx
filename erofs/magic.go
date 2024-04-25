package erofs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"

	"github.com/dnr/styx/common"
	"golang.org/x/sys/unix"
)

const (
	// this doesn't have to match the regular chunk shift
	magicChunkShift common.BlkShift = 20
	magicBlkShift   common.BlkShift = 12
	// offset of start of chunk indexes
	magicOffset = 1024 + 128 + 128 + 64
)

func MagicImageSize(slabSize int64) int64 {
	return (slabSize>>magicChunkShift)*8 + magicOffset
}

func MagicImageRead(devid string, slabBytes int64, off uint64, out []byte) {
	if off < 2<<magicBlkShift {
		start := magicImageStart(devid, slabBytes)
		n := copy(out, start[off:])
		if n < len(out) {
			magicImageChunks(off+uint64(n), out[n:])
		}
	} else {
		magicImageChunks(off, out)
	}
}

func magicImageStart(devid string, slabBytes int64) []byte {
	const inodebase = 1 << magicBlkShift
	const incompat = (EROFS_FEATURE_INCOMPAT_CHUNKED_FILE |
		EROFS_FEATURE_INCOMPAT_DEVICE_TABLE)

	const rootNid = 20
	const slabNid = 40

	// setup super
	super := erofs_super_block{
		Magic:           EROFS_MAGIC,
		FeatureIncompat: incompat,
		BlkSzBits:       common.TruncU8(magicBlkShift),
		RootNid:         common.TruncU16(rootNid),
		Inos:            common.TruncU64(2),
		Blocks:          common.TruncU32(MagicImageSize(slabBytes) >> magicBlkShift),
		MetaBlkAddr:     common.TruncU32(0),
		ExtraDevices:    common.TruncU16(1),
		DevtSlotOff:     (EROFS_SUPER_OFFSET + 128) / 128, // TODO: use constants
	}
	copy(super.VolumeName[:], "@"+devid)
	h := sha256.New()
	h.Write(super.VolumeName[:])
	var hsum [sha256.Size]byte
	copy(super.Uuid[:], h.Sum(hsum[:]))

	c := crc32.NewIEEE()
	pack(c, &super)
	super.Checksum = c.Sum32()

	// dirents
	const numDirents = 3
	dirents := [numDirents]erofs_dirent{
		{Nid: rootNid, NameOff: numDirents*12 + 0, FileType: EROFS_FT_DIR},      // "."
		{Nid: rootNid, NameOff: numDirents*12 + 1, FileType: EROFS_FT_DIR},      // ".."
		{Nid: slabNid, NameOff: numDirents*12 + 3, FileType: EROFS_FT_REG_FILE}, // "slab"
	}
	const direntNames = "...slab"
	const direntSize = len(dirents)*12 + len(direntNames)

	out := bytes.NewBuffer(make([]byte, 0, 2<<magicBlkShift))
	// offset 0
	pad(out, rootNid*32)
	// offset 640

	// nid 20: root dir
	var root erofs_inode_compact
	const layoutCompact = EROFS_INODE_LAYOUT_COMPACT << EROFS_I_VERSION_BIT
	const formatInline = (layoutCompact | (EROFS_INODE_FLAT_INLINE << EROFS_I_DATALAYOUT_BIT))
	root.IFormat = formatInline
	root.IIno = 1
	root.IMode = unix.S_IFDIR | 0755
	root.INlink = 2
	root.ISize = uint32(direntSize)
	pack(out, root)
	// offset 672
	// dirents immediately follow as inline
	pack(out, dirents)
	// offset 708
	out.WriteString(direntNames)
	// offset 715

	// write super
	pad(out, int64(EROFS_SUPER_OFFSET-out.Len()))
	// offset 1024
	pack(out, &super)
	// offset 1152

	// write devtable
	dev := erofs_deviceslot{
		Blocks: common.TruncU32(slabBytes >> magicBlkShift),
	}
	copy(dev.Tag[:], devid)
	pack(out, dev)
	// offset 1280

	// nid 3: slab file
	chunkedIU, err := inodeChunkInfo(magicBlkShift, magicChunkShift)
	if err != nil {
		panic(err)
	}

	var slab erofs_inode_extended
	const layoutExtended = EROFS_INODE_LAYOUT_EXTENDED << EROFS_I_VERSION_BIT
	const formatChunked = (layoutExtended | (EROFS_INODE_CHUNK_BASED << EROFS_I_DATALAYOUT_BIT))
	slab.IFormat = formatChunked
	slab.IIno = slabNid
	slab.IMode = unix.S_IFREG | 0600
	slab.INlink = 1
	slab.ISize = uint64(slabBytes)
	slab.IU = chunkedIU
	pack(out, slab)
	// offset 1344

	if out.Len() != magicOffset {
		panic(fmt.Sprintln("math was wrong", out.Len(), magicOffset))
	}
	// fill in rest with chunk indexes
	pad(out, int64(out.Available()))
	b := out.Bytes()
	magicImageChunks(magicOffset, b[magicOffset:])

	return b
}

func magicImageChunks(off uint64, buf []byte) {
	if off < magicOffset {
		panic("offset too low")
	} else if off%8 != 0 {
		panic("offset must be multiple of 8")
	} else if len(buf)%8 != 0 {
		panic("len must be multiple of 8")
	}
	const devId = 0x00010000 // advise + devid
	startIdx := (off - magicOffset) / 8
	addr := uint64(startIdx<<(magicChunkShift-magicBlkShift+32) | devId)
	inc := uint64(1 << (magicChunkShift - magicBlkShift + 32))
	for len(buf) > 0 {
		binary.LittleEndian.PutUint64(buf, addr)
		addr += inc
		buf = buf[8:]
	}
}
