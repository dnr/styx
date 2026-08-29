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
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/pb"
	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

func slabKey(id uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, id)
	return b
}

func addrKey(addr uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, addr)
	return b
}

func addrFromKey(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}

func locValue(id uint16, addr uint32, sph Sph) []byte {
	loc := make([]byte, 6+sphPrefixBytes)
	binary.LittleEndian.PutUint16(loc, id)
	binary.LittleEndian.PutUint32(loc[2:], addr)
	copy(loc[6:], sph[:sphPrefixBytes])
	return loc
}

func loadLoc(b []byte) erofs.SlabLoc {
	return erofs.SlabLoc{binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[2:])}
}

func appendSph(loc []byte, sph Sph) []byte {
	sphPrefix := sph[:sphPrefixBytes]
	sphs := loc[6:]
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
	s.db.MaxBatchDelay = 100 * time.Millisecond

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
