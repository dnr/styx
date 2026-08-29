package devmapper

import (
	"fmt"
	"io"
	"io/fs"
)

// Volume represents reader/writer for the data handled by the device mapper table.
// Due to constrains the reader and writer operate at SectorSize size.
// Some targets may put further restrictions on the buffer alignment,
// e.g. CryptTarget operates at CryptTarget.SectorSize instead
type Volume interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
}

type volumeRange struct {
	start, len uint64 // in sector size
	volume     Volume
}

// kernel's devmapper handles devices that might have multiple underlying tables, each table having its own range [start,end).
type combinedVolume struct {
	ranges []volumeRange
}

func (c combinedVolume) ReadAt(buf []byte, off int64) (int, error) {
	length := uint64(len(buf))
	offset := uint64(off)

	if length == 0 {
		return 0, nil
	}
	if length%SectorSize != 0 {
		return 0, fmt.Errorf("size of the buffer must be multiple of devmapper.SectorSize")
	}
	if offset%SectorSize != 0 {
		return 0, fmt.Errorf("offset must be multiple of devmapper.SectorSize")
	}

	read := 0
	// the incoming buffer might be split between multiple tables
	for _, r := range c.ranges {
		if offset >= r.start && offset < r.start+r.len {
			rlen := r.start + r.len - offset // this is how much is going to be handled by this range
			if rlen > length {
				rlen = length
			}
			rbuf := buf[:rlen]
			roff := offset - r.start
			n, err := r.volume.ReadAt(rbuf, int64(roff))
			read += n
			if err != nil {
				return read, err
			}
			if uint64(n) != rlen {
				// unable to read full buffer
				return read, nil
			}
			length -= rlen
			offset += rlen
			buf = buf[rlen:]
			if length == 0 {
				// whole requested buffer read
				return read, nil
			}
		}
	}

	return read, fmt.Errorf("unable to find underlying table for offset %d", offset)
}

func (c combinedVolume) WriteAt(buf []byte, off int64) (n int, err error) {
	length := uint64(len(buf))
	offset := uint64(off)

	if length == 0 {
		return 0, nil
	}
	if length%SectorSize != 0 {
		return 0, fmt.Errorf("size of the buffer must be multiple of devmapper.SectorSize")
	}
	if offset%SectorSize != 0 {
		return 0, fmt.Errorf("offset must be multiple of devmapper.SectorSize")
	}

	wrote := 0
	// the incoming buffer might be split between multiple tables
	for _, r := range c.ranges {
		if offset >= r.start && offset < r.start+r.len {
			rlen := r.start + r.len - offset // this is how much is going to be handled by this range
			if rlen > length {
				rlen = length
			}
			rbuf := buf[:rlen]
			roff := offset - r.start
			n, err := r.volume.WriteAt(rbuf, int64(roff))
			wrote += n
			if err != nil {
				return wrote, err
			}
			if uint64(n) != rlen {
				// unable to write full buffer
				return wrote, nil
			}
			length -= rlen
			offset += rlen
			buf = buf[rlen:]
			if length == 0 {
				// whole requested buffer wrote
				return wrote, nil
			}
		}
	}

	return wrote, fmt.Errorf("unable to find underlying table for offset %d", offset)
}

func (c combinedVolume) Close() error {
	for _, r := range c.ranges {
		_ = r.volume.Close()
	}
	return nil
}

// OpenUserspaceVolume opens a volume that allows to read/write data.
// It performs the data processing at user-space level without using device-mapper kernel framework.
// flag and perm parameters are applied to os.OpenFile() when opening the underlying files
func OpenUserspaceVolume(flag int, perm fs.FileMode, tables ...Table) (Volume, error) {
	ranges := make([]volumeRange, len(tables))

	for i, t := range tables {
		ranges[i].start = t.start()
		ranges[i].len = t.length()
		underlying, err := t.openVolume(flag, perm)
		if err != nil {
			return nil, err
		}
		ranges[i].volume = underlying
	}

	return &combinedVolume{ranges: ranges}, nil
}
