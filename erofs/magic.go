package erofs

import (
	"bytes"
	"crypto/sha256"
	"hash/crc32"

	"github.com/dnr/styx/common"
	"golang.org/x/sys/unix"
)

const (
	magicBlkShift common.BlkShift = 12
)

func MagicImageSize(slabSize int64) int64 {
	return 1 << magicBlkShift
}

func MagicImageRead(devid string, slabBytes int64, off uint64, out []byte) {
	block := magicImageStart(devid, slabBytes)
	copy(out, block[off:])
}

func magicImageStart(devid string, slabBytes int64) []byte {
	const inodebase = 1 << magicBlkShift
	const incompat = EROFS_FEATURE_INCOMPAT_DEVICE_TABLE

	const rootNid = 20
	const slabNid = 40
	const mappedBlkAddr = 1

	// setup super
	super := erofs_super_block{
		Magic:           EROFS_MAGIC,
		FeatureIncompat: incompat,
		BlkSzBits:       common.TruncU8(magicBlkShift),
		RootNid:         common.TruncU16(rootNid),
		Inos:            common.TruncU64(2),
		Blocks:          common.TruncU32(MagicImageSize(slabBytes)>>magicBlkShift + 1),
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

	// setup dirents
	const numDirents = 3
	dirents := [numDirents]erofs_dirent{
		{Nid: rootNid, NameOff: numDirents*12 + 0, FileType: EROFS_FT_DIR},      // "."
		{Nid: rootNid, NameOff: numDirents*12 + 1, FileType: EROFS_FT_DIR},      // ".."
		{Nid: slabNid, NameOff: numDirents*12 + 3, FileType: EROFS_FT_REG_FILE}, // "slab"
	}
	const direntNames = "...slab"
	const direntSize = len(dirents)*12 + len(direntNames)

	out := bytes.NewBuffer(make([]byte, 0, 1<<magicBlkShift))
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
		Blocks:        common.TruncU32(slabBytes >> magicBlkShift),
		MappedBlkAddr: common.TruncU32(mappedBlkAddr),
	}
	copy(dev.Tag[:], devid)
	pack(out, dev)
	// offset 1280

	// nid 40: slab file
	var slab erofs_inode_extended
	const layoutExtended = EROFS_INODE_LAYOUT_EXTENDED << EROFS_I_VERSION_BIT
	const formatPlainExt = (layoutExtended | (EROFS_INODE_FLAT_PLAIN << EROFS_I_DATALAYOUT_BIT))
	slab.IFormat = formatPlainExt
	slab.IIno = slabNid
	slab.IMode = unix.S_IFREG | 0400
	slab.INlink = 1
	slab.ISize = uint64(slabBytes)
	slab.IU = common.TruncU32(mappedBlkAddr)
	pack(out, slab)
	// offset 1344

	// fill in rest with zero
	pad(out, int64(out.Available()))
	return out.Bytes()
}
