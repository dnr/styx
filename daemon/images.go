package daemon

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"go.etcd.io/bbolt"
)

func (s *Server) imageSlabPath() string {
	return filepath.Join(s.cfg.CachePath, slabSubdir, imageSlab)
}

func (s *Server) setupImageSlab() error {
	path := s.imageSlabPath()

	if err := ensureRegularFileSize(path, slabBytes); err != nil {
		return err
	}
	lo, err := s.locache.findOrAttach(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(lo.Path(), os.O_RDWR, 0)
	if err != nil {
		return err
	}

	s.imageSlabLo = lo
	s.imageSlabF = f
	return nil
}

func (s *Server) allocateImageSpace(imgBlocks uint32) (imgOff uint32, retErr error) {
	retErr = s.db.Update(func(tx *bbolt.Tx) error {
		if v := tx.Bucket(metaBucket).Get(metaImageOffset); v == nil {
			imgOff = reservedBlocks
		} else {
			imgOff = binary.LittleEndian.Uint32(v)
		}
		nextOff := imgOff + uint32(imgBlocks)
		v := binary.LittleEndian.AppendUint32(nil, nextOff)
		return tx.Bucket(metaBucket).Put(metaImageOffset, v)
	})
	return
}
