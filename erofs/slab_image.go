package erofs

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"hash/crc32"

	"github.com/dnr/styx/common"
	"golang.org/x/sys/unix"
)

func SlabImageRead(devid string, slabBytes int64, blkShift common.BlkShift, off uint64, buf []byte) error {
	if off != 0 {
		return errors.New("slab image read must be from start")
	} else if len(buf) < 4096 {
		return errors.New("slab image read must be at least 4k")
	}

	const incompat = EROFS_FEATURE_INCOMPAT_DEVICE_TABLE

	const rootNid = 20
	const slabNid = 24
	const mappedBlkAddr = 1

	// setup super
	super := erofs_super_block{
		Magic:           EROFS_MAGIC,
		FeatureIncompat: incompat,
		BlkSzBits:       common.TruncU8(blkShift),
		RootNid:         common.TruncU16(rootNid),
		Inos:            common.TruncU64(2),
		Blocks:          common.TruncU32(1),
		MetaBlkAddr:     common.TruncU32(0),
		ExtraDevices:    common.TruncU16(1),
		DevtSlotOff:     (EROFS_SUPER_OFFSET +EROFS_SUPER_SIZE ) / EROFS_DEVT_SLOT_SIZE,
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

	// writes to out will fill in buf
	out := bytes.NewBuffer(buf[:0:len(buf)])
	// offset 0
	pad(out, rootNid<<EROFS_NID_SHIFT)
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

	// nid 24: slab file
	pad(out, int64(slabNid<<EROFS_NID_SHIFT-out.Len()))
	// offset 768
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
	// offset 832

	// write super
	pad(out, int64(EROFS_SUPER_OFFSET-out.Len()))
	// offset 1024
	pack(out, &super)
	// offset 1152

	// write devtable
	dev := erofs_deviceslot{
		Blocks:        common.TruncU32(slabBytes >> blkShift),
		MappedBlkAddr: common.TruncU32(mappedBlkAddr),
	}
	copy(dev.Tag[:], devid)
	pack(out, dev)
	// offset 1280

	// fill in rest with zero
	pad(out, int64(out.Available()))
	return nil
}
