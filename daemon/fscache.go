package daemon

// References:
// https://www.kernel.org/doc/html/latest/filesystems/caching/cachefiles.html

type (
	cachefiles_msg struct {
		//	struct cachefiles_msg {
		//		__u32 msg_id;
		MsgId uint32 `struc:"little"`
		//		__u32 opcode;
		OpCode uint32 `struc:"little"`
		//		__u32 len;
		Len uint32 `struc:"little"`
		//		__u32 object_id;
		ObjectId uint32 `struc:"little"`
		//		__u8  data[];
		//	};
	}

	cachefiles_open struct {
		// struct cachefiles_open {
		// 	__u32 volume_key_size;
		VolumeKeySize uint32 `struc:"little,sizeof=VolumeKey"`
		// 	__u32 cookie_key_size;
		CookieKeySize uint32 `struc:"little,sizeof=CookieKey"`
		// 	__u32 fd;
		Fd uint32 `struc:"little"`
		// 	__u32 flags;
		Flags uint32 `struc:"little"`
		// 	__u8  data[];
		VolumeKey []byte
		CookieKey []byte
		// };
	}
	// data contains the volume_key followed directly by the cookie_key. The volume key is a
	// NUL-terminated string; the cookie key is binary data.
	// volume_key_size indicates the size of the volume key in bytes.
	// cookie_key_size indicates the size of the cookie key in bytes.
	// fd indicates an anonymous fd referring to the cache file, through which the user daemon can
	// perform write/llseek file operations on the cache file.

	// The user daemon should reply the OPEN request by issuing a "copen" (complete open)
	// command on the devnode:
	//   copen <msg_id>,<cache_size>
	// msg_id must match the msg_id field of the OPEN request.
	// When >= 0, cache_size indicates the size of the cache file; when < 0, cache_size
	// indicates any error code encountered by the user daemon.

	// When a cookie withdrawn, a CLOSE request (opcode CACHEFILES_OP_CLOSE) will be sent to
	// the user daemon. This tells the user daemon to close all anonymous fds associated with
	// the given object_id. The CLOSE request has no extra payload, and shouldn't be replied.

	cachefiles_read struct {
		// struct cachefiles_read {
		// 	__u64 off;
		Off uint64 `struc:"little"`
		// 	__u64 len;
		Len uint64 `struc:"little"`
		// };
	}
	// off indicates the starting offset of the requested file range.
	// len indicates the length of the requested file range.

	// When it receives a READ request, the user daemon should fetch the requested data and
	// write it to the cache file identified by object_id.
	// When it has finished processing the READ request, the user daemon should reply by using
	// the CACHEFILES_IOC_READ_COMPLETE ioctl on one of the anonymous fds associated with the
	// object_id given in the READ request. The ioctl is of the form:
	// ioctl(fd, CACHEFILES_IOC_READ_COMPLETE, msg_id);
	// where:
	// fd is one of the anonymous fds associated with the object_id given.
	// msg_id must match the msg_id field of the READ request.
)

const (
	/*
	 * Fscache ensures that the maximum length of cookie key is 255. The volume key
	 * is controlled by netfs, and generally no bigger than 255.
	 */
	CACHEFILES_MSG_MAX_SIZE = 1024

	CACHEFILES_OP_OPEN  = 0
	CACHEFILES_OP_CLOSE = 1
	CACHEFILES_OP_READ  = 2

	CACHEFILES_IOC_READ_COMPLETE = _IOC_WRITE<<_IOC_DIRSHIFT | 0x98<<_IOC_TYPESHIFT | 1<<_IOC_NRSHIFT | 4<<_IOC_SIZESHIFT

	_IOC_WRITE     = 1
	_IOC_NRBITS    = 8
	_IOC_TYPEBITS  = 8
	_IOC_SIZEBITS  = 14
	_IOC_DIRBITS   = 2
	_IOC_NRSHIFT   = 0
	_IOC_TYPESHIFT = _IOC_NRSHIFT + _IOC_NRBITS
	_IOC_SIZESHIFT = _IOC_TYPESHIFT + _IOC_TYPEBITS
	_IOC_DIRSHIFT  = _IOC_SIZESHIFT + _IOC_SIZEBITS
)
