package erofs

import (
	"fmt"

	"github.com/dnr/styx/common/shift"
	"github.com/lunixbochs/struc"
)

// References:
// https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/tree/fs/erofs/erofs_fs.h?h=v6.6
// https://erofs.docs.kernel.org/en/latest/core_ondisk.html

const (
	EROFS_MAGIC = 0xE0F5E1E2

	EROFS_SUPER_OFFSET = 1024

	EROFS_FEATURE_COMPAT_SB_CHKSUM    = 0x00000001
	EROFS_FEATURE_COMPAT_MTIME        = 0x00000002
	EROFS_FEATURE_COMPAT_XATTR_FILTER = 0x00000004

	/*
	 * Any bits that aren't in EROFS_ALL_FEATURE_INCOMPAT should
	 * be incompatible with this kernel version.
	 */
	EROFS_FEATURE_INCOMPAT_ZERO_PADDING   = 0x00000001
	EROFS_FEATURE_INCOMPAT_COMPR_CFGS     = 0x00000002
	EROFS_FEATURE_INCOMPAT_BIG_PCLUSTER   = 0x00000002
	EROFS_FEATURE_INCOMPAT_CHUNKED_FILE   = 0x00000004
	EROFS_FEATURE_INCOMPAT_DEVICE_TABLE   = 0x00000008
	EROFS_FEATURE_INCOMPAT_COMPR_HEAD2    = 0x00000008
	EROFS_FEATURE_INCOMPAT_ZTAILPACKING   = 0x00000010
	EROFS_FEATURE_INCOMPAT_FRAGMENTS      = 0x00000020
	EROFS_FEATURE_INCOMPAT_DEDUPE         = 0x00000020
	EROFS_FEATURE_INCOMPAT_XATTR_PREFIXES = 0x00000040
	EROFS_ALL_FEATURE_INCOMPAT            = (EROFS_FEATURE_INCOMPAT_ZERO_PADDING |
		EROFS_FEATURE_INCOMPAT_COMPR_CFGS |
		EROFS_FEATURE_INCOMPAT_BIG_PCLUSTER |
		EROFS_FEATURE_INCOMPAT_CHUNKED_FILE |
		EROFS_FEATURE_INCOMPAT_DEVICE_TABLE |
		EROFS_FEATURE_INCOMPAT_COMPR_HEAD2 |
		EROFS_FEATURE_INCOMPAT_ZTAILPACKING |
		EROFS_FEATURE_INCOMPAT_FRAGMENTS |
		EROFS_FEATURE_INCOMPAT_DEDUPE |
		EROFS_FEATURE_INCOMPAT_XATTR_PREFIXES)

	EROFS_SB_EXTSLOT_SIZE = 16

	EROFS_DEVT_SLOT_SIZE = 128

	/*
	 * EROFS inode datalayout (i_format in on-disk inode):
	 * 0 - uncompressed flat inode without tail-packing inline data:
	 * 1 - compressed inode with non-compact indexes:
	 * 2 - uncompressed flat inode with tail-packing inline data:
	 * 3 - compressed inode with compact indexes:
	 * 4 - chunk-based inode with (optional) multi-device support:
	 * 5~7 - reserved
	 */
	EROFS_INODE_FLAT_PLAIN         = 0
	EROFS_INODE_COMPRESSED_FULL    = 1
	EROFS_INODE_FLAT_INLINE        = 2
	EROFS_INODE_COMPRESSED_COMPACT = 3
	EROFS_INODE_CHUNK_BASED        = 4
	EROFS_INODE_DATALAYOUT_MAX     = 5

	/* bit definitions of inode i_format */
	EROFS_I_VERSION_MASK    = 0x01
	EROFS_I_DATALAYOUT_MASK = 0x07

	EROFS_I_VERSION_BIT    = 0
	EROFS_I_DATALAYOUT_BIT = 1
	EROFS_I_ALL_BIT        = 4

	EROFS_I_ALL = ((1 << EROFS_I_ALL_BIT) - 1)

	/* indicate chunk blkbits, thus 'chunksize = blocksize << chunk blkbits' */
	EROFS_CHUNK_FORMAT_BLKBITS_MASK = 0x001F
	/* with chunk indexes or just a 4-byte blkaddr array */
	EROFS_CHUNK_FORMAT_INDEXES = 0x0020

	EROFS_CHUNK_FORMAT_ALL = (EROFS_CHUNK_FORMAT_BLKBITS_MASK | EROFS_CHUNK_FORMAT_INDEXES)

	/* 32-byte on-disk inode */
	EROFS_INODE_LAYOUT_COMPACT = 0
	/* 64-byte on-disk inode */
	EROFS_INODE_LAYOUT_EXTENDED = 1

	/* represent a zeroed chunk (hole) */
	EROFS_NULL_ADDR = -1

	/* 4-byte block address array */
	EROFS_BLOCK_MAP_ENTRY_SIZE = 4

	EROFS_NAME_LEN = 255

	/* maximum supported size of a physical compression cluster */
	Z_EROFS_PCLUSTER_MAX_SIZE = (1024 * 1024)

	/* available compression algorithm types (for h_algorithmtype) */
	Z_EROFS_COMPRESSION_LZ4     = 0
	Z_EROFS_COMPRESSION_LZMA    = 1
	Z_EROFS_COMPRESSION_DEFLATE = 2
	Z_EROFS_COMPRESSION_MAX     = 3
	// Z_EROFS_ALL_COMPR_ALGS		((1 << Z_EROFS_COMPRESSION_MAX) - 1)

	/*
	 * bit 0 : COMPACTED_2B indexes (0 - off; 1 - on)
	 *  e.g. for 4k logical cluster size,      4B        if compacted 2B is off;
	 *                                  (4B) + 2B + (4B) if compacted 2B is on.
	 * bit 1 : HEAD1 big pcluster (0 - off; 1 - on)
	 * bit 2 : HEAD2 big pcluster (0 - off; 1 - on)
	 * bit 3 : tailpacking inline pcluster (0 - off; 1 - on)
	 * bit 4 : interlaced plain pcluster (0 - off; 1 - on)
	 * bit 5 : fragment pcluster (0 - off; 1 - on)
	 */
	Z_EROFS_ADVISE_COMPACTED_2B        = 0x0001
	Z_EROFS_ADVISE_BIG_PCLUSTER_1      = 0x0002
	Z_EROFS_ADVISE_BIG_PCLUSTER_2      = 0x0004
	Z_EROFS_ADVISE_INLINE_PCLUSTER     = 0x0008
	Z_EROFS_ADVISE_INTERLACED_PCLUSTER = 0x0010
	Z_EROFS_ADVISE_FRAGMENT_PCLUSTER   = 0x0020

	Z_EROFS_FRAGMENT_INODE_BIT = 7

	/*
	 * On-disk logical cluster type:
	 *    0   - literal (uncompressed) lcluster
	 *    1,3 - compressed lcluster (for HEAD lclusters)
	 *    2   - compressed lcluster (for NONHEAD lclusters)
	 *
	 * In detail,
	 *    0 - literal (uncompressed) lcluster,
	 *        di_advise = 0
	 *        di_clusterofs = the literal data offset of the lcluster
	 *        di_blkaddr = the blkaddr of the literal pcluster
	 *
	 *    1,3 - compressed lcluster (for HEAD lclusters)
	 *        di_advise = 1 or 3
	 *        di_clusterofs = the decompressed data offset of the lcluster
	 *        di_blkaddr = the blkaddr of the compressed pcluster
	 *
	 *    2 - compressed lcluster (for NONHEAD lclusters)
	 *        di_advise = 2
	 *        di_clusterofs =
	 *           the decompressed data offset in its own HEAD lcluster
	 *        di_u.delta[0] = distance to this HEAD lcluster
	 *        di_u.delta[1] = distance to the next HEAD lcluster
	 */
	Z_EROFS_LCLUSTER_TYPE_PLAIN   = 0
	Z_EROFS_LCLUSTER_TYPE_HEAD1   = 1
	Z_EROFS_LCLUSTER_TYPE_NONHEAD = 2
	Z_EROFS_LCLUSTER_TYPE_HEAD2   = 3
	Z_EROFS_LCLUSTER_TYPE_MAX     = 4

	Z_EROFS_LI_LCLUSTER_TYPE_BITS = 2
	Z_EROFS_LI_LCLUSTER_TYPE_BIT  = 0

	/* (noncompact only, HEAD) This pcluster refers to partial decompressed data */
	// Z_EROFS_LI_PARTIAL_REF		(1 << 15)

	/*
	 * D0_CBLKCNT will be marked _only_ at the 1st non-head lcluster to store the
	 * compressed block count of a compressed extent (in logical clusters, aka.
	 * block count of a pcluster).
	 */
	Z_EROFS_LI_D0_CBLKCNT = (1 << 11)

	EROFS_FT_UNKNOWN  = 0
	EROFS_FT_REG_FILE = 1
	EROFS_FT_DIR      = 2
	EROFS_FT_CHRDEV   = 3
	EROFS_FT_BLKDEV   = 4
	EROFS_FT_FIFO     = 5
	EROFS_FT_SOCK     = 6
	EROFS_FT_SYMLINK  = 7
	EROFS_FT_MAX      = 8

	// added, not in header file:
	EROFS_SUPER_SIZE          = 128
	EROFS_COMPACT_INODE_SIZE  = 32
	EROFS_EXTENDED_INODE_SIZE = 64
	EROFS_NID_SHIFT           = shift.Shift(5)
)

type (
	erofs_deviceslot struct {
		// struct erofs_deviceslot {
		Tag [64]byte
		// 	u8 tag[64];		/* digest(sha256), etc. */
		// 	__le32 blocks;		/* total fs blocks of this device */
		Blocks uint32
		// 	__le32 mapped_blkaddr;	/* map starting at mapped_blkaddr */
		MappedBlkAddr uint32
		// 	u8 reserved[56];
		Reserved [56]byte
		// };
	}

	erofs_super_block struct {
		// struct erofs_super_block {
		// 	__le32 magic;           /* file system magic number */
		Magic uint32
		// 	__le32 checksum;        /* crc32c(super_block) */
		Checksum uint32
		// 	__le32 feature_compat;
		FeatureCompat uint32
		// 	__u8 blkszbits;         /* filesystem block size in bit shift */
		BlkSzBits uint8
		// 	__u8 sb_extslots;	/* superblock size = 128 + sb_extslots * 16 */
		SbExtSlots uint8
		// 	__le16 root_nid;	/* nid of root directory */
		RootNid uint16
		// 	__le64 inos;            /* total valid ino # (== f_files - f_favail) */
		Inos uint64

		// 	__le64 build_time;      /* compact inode time derivation */
		BuildTime uint64
		// 	__le32 build_time_nsec;	/* compact inode time derivation in ns scale */
		BuildTimeNsec uint32
		// 	__le32 blocks;          /* used for statfs */
		Blocks uint32
		// 	__le32 meta_blkaddr;	/* start block address of metadata area */
		MetaBlkAddr uint32
		// 	__le32 xattr_blkaddr;	/* start block address of shared xattr area */
		XattrBlkAddr uint32
		// 	__u8 uuid[16];          /* 128-bit uuid for volume */
		Uuid [16]byte
		// 	__u8 volume_name[16];   /* volume name */
		VolumeName [16]byte
		// 	__le32 feature_incompat;
		FeatureIncompat uint32
		// 	union {
		// 		/* bitmap for available compression algorithms */
		// 		__le16 available_compr_algs;
		// 		/* customized sliding window size instead of 64k by default */
		// 		__le16 lz4_max_distance;
		// 	} __packed u1;
		U1 uint16
		// 	__le16 extra_devices;	/* # of devices besides the primary device */
		ExtraDevices uint16
		// 	__le16 devt_slotoff;	/* startoff = devt_slotoff * devt_slotsize */
		DevtSlotOff uint16
		// 	__u8 dirblkbits;	/* directory block size in bit shift */
		DirBlkBits uint8
		// 	__u8 xattr_prefix_count;	/* # of long xattr name prefixes */
		XttrPrefixCount uint8
		// 	__le32 xattr_prefix_start;	/* start of long xattr prefixes */
		XattrPrefixStart uint32
		// 	__le64 packed_nid;	/* nid of the special packed inode */
		PackedNid uint64
		// 	__u8 xattr_filter_reserved; /* reserved for xattr name filter */
		XattrFilterReserved uint8
		// 	__u8 reserved2[23];
		Reserved2 [23]byte
		// };
	}

	erofs_inode_chunk_info struct {
		//	struct erofs_inode_chunk_info {
		//		__le16 format;		/* chunk blkbits, etc. */
		Format uint16
		// __le16 reserved;
		Reserved uint16
		// };
	}

	// static inline bool erofs_inode_is_data_compressed(unsigned int datamode)
	// {
	// 	return datamode == EROFS_INODE_COMPRESSED_COMPACT ||
	// 		datamode == EROFS_INODE_COMPRESSED_FULL;
	// }

	// union erofs_inode_i_u {
	// 	/* total compressed blocks for compressed inodes */
	// 	__le32 compressed_blocks;
	// 	/* block address for uncompressed flat inodes */
	// 	__le32 raw_blkaddr;
	// 	/* for device files, used to indicate old/new device # */
	// 	__le32 rdev;
	// 	/* for chunk-based files, it contains the summary info */
	// 	struct erofs_inode_chunk_info c;
	// };

	erofs_inode_compact struct {
		// /* 32-byte reduced form of an ondisk inode */
		// struct erofs_inode_compact {
		// 	__le16 i_format;	/* inode format hints */
		IFormat uint16

		// 	/* 1 header + n-1 * 4 bytes inline xattr to keep continuity */
		// 	__le16 i_xattr_icount;
		IXattrICount uint16
		// 	__le16 i_mode;
		IMode uint16
		// 	__le16 i_nlink;
		INlink uint16
		// 	__le32 i_size;
		ISize uint32
		// 	__le32 i_reserved;
		IReserved uint32
		// 	union erofs_inode_i_u i_u;
		IU uint32
		// 	__le32 i_ino;		/* only used for 32-bit stat compatibility */
		IIno uint32
		// 	__le16 i_uid;
		IUid uint16
		// 	__le16 i_gid;
		IGid uint16
		// 	__le32 i_reserved2;
		IReserved2 uint32
		// };
	}

	erofs_inode_extended struct {
		// /* 64-byte complete form of an ondisk inode */
		// struct erofs_inode_extended {
		// 	__le16 i_format;	/* inode format hints */
		IFormat uint16

		// 	/* 1 header + n-1 * 4 bytes inline xattr to keep continuity */
		// 	__le16 i_xattr_icount;
		IXattrICount uint16
		// 	__le16 i_mode;
		IMode uint16
		// 	__le16 i_reserved;
		IReserved uint16
		// 	__le64 i_size;
		ISize uint64
		// 	union erofs_inode_i_u i_u;
		IU uint32

		// 	__le32 i_ino;		/* only used for 32-bit stat compatibility */
		IIno uint32
		// 	__le32 i_uid;
		IUid uint32
		// 	__le32 i_gid;
		IGid uint32
		// 	__le64 i_mtime;
		IMtime uint64
		// 	__le32 i_mtime_nsec;
		IMtimeNsec uint32
		// 	__le32 i_nlink;
		INlink uint32
		// 	__u8   i_reserved2[16];
		IReserved2 [16]byte
		// };
	}

	erofs_inode_chunk_index struct {
		// /* 8-byte inode chunk indexes */
		// struct erofs_inode_chunk_index {
		// 	__le16 advise;		/* always 0, don't care for now */
		Advise uint16
		// 	__le16 device_id;	/* back-end storage id (with bits masked) */
		DeviceId uint16
		// 	__le32 blkaddr;		/* start block address of this inode chunk */
		BlkAddr uint32
		// };
	}

	erofs_dirent struct {
		// /* dirent sorts in alphabet order, thus we can do binary search */
		//
		//	struct erofs_dirent {
		//		__le64 nid;     /* node number */
		Nid uint64
		// __le16 nameoff; /* start offset of file name */
		NameOff uint16
		// __u8 file_type; /* file type */
		FileType uint8
		// __u8 reserved;  /* reserved */
		Reserved uint8
		// } __packed;
	}

	// /* 14 bytes (+ length field = 16 bytes) */
	// struct z_erofs_lz4_cfgs {
	// 	__le16 max_distance;
	// 	__le16 max_pclusterblks;
	// 	u8 reserved[10];
	// } __packed;

	// /* 14 bytes (+ length field = 16 bytes) */
	// struct z_erofs_lzma_cfgs {
	// 	__le32 dict_size;
	// 	__le16 format;
	// 	u8 reserved[8];
	// } __packed;

	// #define Z_EROFS_LZMA_MAX_DICT_SIZE	(8 * Z_EROFS_PCLUSTER_MAX_SIZE)

	// /* 6 bytes (+ length field = 8 bytes) */
	// struct z_erofs_deflate_cfgs {
	// 	u8 windowbits;			/* 8..15 for DEFLATE */
	// 	u8 reserved[5];
	// } __packed;

	// struct z_erofs_map_header {
	// 	union {
	// 		/* fragment data offset in the packed inode */
	// 		__le32  h_fragmentoff;
	// 		struct {
	// 			__le16  h_reserved1;
	// 			/* indicates the encoded size of tailpacking data */
	// 			__le16  h_idata_size;
	// 		};
	// 	};
	// 	__le16	h_advise;
	// 	/*
	// 	 * bit 0-3 : algorithm type of head 1 (logical cluster type 01);
	// 	 * bit 4-7 : algorithm type of head 2 (logical cluster type 11).
	// 	 */
	// 	__u8	h_algorithmtype;
	// 	/*
	// 	 * bit 0-2 : logical cluster bits - 12, e.g. 0 for 4096;
	// 	 * bit 3-6 : reserved;
	// 	 * bit 7   : move the whole file into packed inode or not.
	// 	 */
	// 	__u8	h_clusterbits;
	// };

	// struct z_erofs_lcluster_index {
	// 	__le16 di_advise;
	// 	/* where to decompress in the head lcluster */
	// 	__le16 di_clusterofs;

	// 	union {
	// 		/* for the HEAD lclusters */
	// 		__le32 blkaddr;
	// 		/*
	// 		 * for the NONHEAD lclusters
	// 		 * [0] - distance to its HEAD lcluster
	// 		 * [1] - distance to the next HEAD lcluster
	// 		 */
	// 		__le16 delta[2];
	// 	} di_u;
	// };

	//	#define Z_EROFS_FULL_INDEX_ALIGN(end)	\
	//		(ALIGN(end, 8) + sizeof(struct z_erofs_map_header) + 8)
)

// static inline void erofs_check_ondisk_layout_definitions(void)
func init() {
	checkSize := func(v any, expected int) {
		if s, err := struc.Sizeof(v); err != nil {
			panic(err)
		} else if s != expected {
			panic(fmt.Sprintf("size of %T should be %d but is %d", v, expected, s))
		}
	}

	// 	const __le64 fmh = *(__le64 *)&(struct z_erofs_map_header) {
	// 		.h_clusterbits = 1 << Z_EROFS_FRAGMENT_INODE_BIT
	// 	};
	// 	BUILD_BUG_ON(sizeof(struct erofs_super_block) != 128);
	checkSize(&erofs_super_block{}, EROFS_SUPER_SIZE)
	// 	BUILD_BUG_ON(sizeof(struct erofs_inode_compact) != 32);
	checkSize(&erofs_inode_compact{}, EROFS_COMPACT_INODE_SIZE)
	// 	BUILD_BUG_ON(sizeof(struct erofs_inode_extended) != 64);
	checkSize(&erofs_inode_extended{}, EROFS_EXTENDED_INODE_SIZE)
	// 	BUILD_BUG_ON(sizeof(struct erofs_xattr_ibody_header) != 12);
	// 	BUILD_BUG_ON(sizeof(struct erofs_xattr_entry) != 4);
	// 	BUILD_BUG_ON(sizeof(struct erofs_inode_chunk_info) != 4);
	checkSize(&erofs_inode_chunk_info{}, 4)
	// 	BUILD_BUG_ON(sizeof(struct erofs_inode_chunk_index) != 8);
	checkSize(&erofs_inode_chunk_index{}, 8)
	// 	BUILD_BUG_ON(sizeof(struct z_erofs_map_header) != 8);
	// 	BUILD_BUG_ON(sizeof(struct z_erofs_lcluster_index) != 8);
	// 	BUILD_BUG_ON(sizeof(struct erofs_dirent) != 12);
	checkSize(&erofs_dirent{}, 12)
	// 	/* keep in sync between 2 index structures for better extendibility */
	// 	BUILD_BUG_ON(sizeof(struct erofs_inode_chunk_index) !=
	// 		     sizeof(struct z_erofs_lcluster_index));
	// 	BUILD_BUG_ON(sizeof(struct erofs_deviceslot) != 128);
	checkSize(&erofs_deviceslot{}, EROFS_DEVT_SLOT_SIZE)

	//		BUILD_BUG_ON(BIT(Z_EROFS_LI_LCLUSTER_TYPE_BITS) <
	//			     Z_EROFS_LCLUSTER_TYPE_MAX - 1);
	//		/* exclude old compiler versions like gcc 7.5.0 */
	//		BUILD_BUG_ON(__builtin_constant_p(fmh) ?
	//			     fmh != cpu_to_le64(1ULL << 63) : 0);
	//	}
}
