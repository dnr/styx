package daemon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

// slab id -> key in slabBucket
func slabKey(slabId uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, slabId)
	return b
}

// addr -> key in buckets in slabBucket
func addrKey(addr uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, addr)
	return b
}

// key in buckets in slabBucket -> addr
func addrFromKey(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}

// length in blocks, digest -> value in buckets in slabBucket
func slabValue(blocks uint16, dig cdig.CDig) []byte {
	b := make([]byte, 2+cdig.Bytes)
	binary.LittleEndian.PutUint16(b[0:2], blocks)
	copy(b[2:], dig[:])
	return b
}

// value in buckets in slabBucket -> length in blocks, digest
func loadSlab(b []byte) (uint16, cdig.CDig) {
	blocks := binary.LittleEndian.Uint16(b)
	dig := cdig.FromBytes(b[2:])
	return blocks, dig
}

// slab id, addr, blocks, first sph -> value in chunk bucket
func locValue(slabId uint16, addr uint32, blocks uint16, sph Sph) []byte {
	loc := make([]byte, 8+sphPrefixBytes)
	binary.LittleEndian.PutUint16(loc, slabId)
	binary.LittleEndian.PutUint32(loc[2:], addr)
	binary.LittleEndian.PutUint16(loc[6:], blocks)
	copy(loc[8:], sph[:sphPrefixBytes])
	return loc
}

// value in chunk bucket -> slab id, addr
func loadLoc(b []byte) erofs.SlabLoc {
	return erofs.SlabLoc{binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[2:])}
}

// value in chunk bucket -> slab id, addr
func loadLocAndBlocks(b []byte) (erofs.SlabLoc, uint16) {
	loc := erofs.SlabLoc{binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[2:])}
	blocks := binary.LittleEndian.Uint16(b[6:])
	return loc, blocks
}

func appendSph(loc []byte, sph Sph) []byte {
	sphPrefix := sph[:sphPrefixBytes]
	sphs := loc[8:]
	for len(sphs) >= sphPrefixBytes {
		if bytes.Equal(sphs[:sphPrefixBytes], sphPrefix) {
			return nil
		}
		sphs = sphs[sphPrefixBytes:]
	}
	newLoc := make([]byte, len(loc)+sphPrefixBytes)
	copy(newLoc, loc)
	copy(newLoc[len(loc):], sphPrefix)
	return newLoc
}

func (s *Server) openDb() (err error) {
	opts := bbolt.Options{
		NoFreelistSync: true,
		FreelistType:   bbolt.FreelistMapType,
	}

	dbPath := filepath.Join(s.cfg.CachePath, dbFilename)

	if os.Remove(filepath.Join(s.cfg.CachePath, compactFile)) == nil {
		// request to compact db
		ctime := time.Now().UTC().Format(time.RFC3339)
		newPath := dbPath + ".new." + ctime
		cmpPath := dbPath + ".compacted." + ctime
		if newDb, err := bbolt.Open(newPath, 0644, &opts); err == nil {
			if oldDb, err := bbolt.Open(dbPath, 0644, &opts); err == nil {
				if err := bbolt.Compact(newDb, oldDb, 4<<20); err == nil {
					oldDb.Close()
					newDb.Close()
					if os.Rename(dbPath, cmpPath) == nil {
						os.Rename(newPath, dbPath)
						log.Println("compacted db, old file in", cmpPath)
					}
				} else {
					log.Println("bolt compact error:", err)
				}
				oldDb.Close()
			}
			newDb.Close()
		}
	}

	s.db, err = bbolt.Open(dbPath, 0644, &opts)
	if err != nil {
		return err
	}
	// we only use batching for updating the presence map, which is a background thing,
	// so this can take longer.
	s.db.MaxBatchDelay = 1000 * time.Millisecond

	checkSchemaVer := func(mb *bbolt.Bucket) error {
		b := mb.Get(metaSchema)
		if len(b) != 4 {
			ver := binary.LittleEndian.AppendUint32(nil, schemaLatest)
			return mb.Put(metaSchema, ver)
		}
		have := binary.LittleEndian.Uint32(b)
		if have != schemaLatest {
			return fmt.Errorf("mismatched schema version %d != %d", have, schemaLatest)
		}
		return nil
	}

	loadParams := func(mb *bbolt.Bucket) error {
		b := mb.Get(metaParams)
		if b == nil {
			// no params yet, leave uninitialized
			log.Print("initializing with empty config, call 'styx init --params=...'")
			return nil
		}
		var dp pb.DbParams
		if err := proto.Unmarshal(b, &dp); err != nil {
			return err
		}
		if err := verifyParams(dp.Params.Params); err != nil {
			return err
		}
		keys, err := common.LoadPubKeys(dp.Pubkey)
		if err != nil {
			return err
		}
		return s.postInit(dp.Params, keys)
	}

	return s.db.Update(func(tx *bbolt.Tx) error {
		if mb, err := tx.CreateBucketIfNotExists(metaBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(chunkBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(slabBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(imageBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(manifestBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(catalogFBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(catalogRBucket); err != nil {
			return err
		} else if _, err = tx.CreateBucketIfNotExists(fakeCacheBucket); err != nil {
			return err
		} else if err = checkSchemaVer(mb); err != nil {
			return err
		} else if err = loadParams(mb); err != nil {
			return err
		}
		return nil
	})
}
