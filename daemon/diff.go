package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

type (
	digestIterator struct {
		ents       []*pb.Entry
		digestLen  int
		chunkShift blkshift
		e          int
		d          int
	}
)

func (s *server) requestDigest(slabId uint16, addr uint32, digest []byte, sphs []Sph) chan error {
	ctx := context.Background()
	ch := make(chan error)
	go func() {
		err := s.tryDiff(ctx, slabId, addr, digest, sphs)
		if err != nil {
			log.Printf("tryDiff(%s): %v, falling back to single read", common.DigestStr(digest), err)
			err = s.readSingle(ctx, slabId, addr, digest)
		}
		ch <- err
	}()
	return ch
}

func (s *server) readSingle(ctx context.Context, slabId uint16, addr uint32, digest []byte) error {
	buf := s.chunkPool.Get().([]byte)
	defer s.chunkPool.Put(buf)

	digestStr := common.DigestStr(digest)
	chunk, err := s.csread.Get(ctx, digestStr, buf[:0])
	if err != nil {
		return err
	} else if len(chunk) > len(buf) || &buf[0] != &chunk[0] {
		return fmt.Errorf("chunk overflowed chunk size: %d > %d", len(chunk), len(buf))
	}

	return s.gotNewChunk(slabId, addr, digest, chunk)
}

func (s *server) tryDiff(ctx context.Context, slabId uint16, addr uint32, targetDigest []byte, sphs []Sph) error {
	// TODO: able to use multiple bases at once
	res, reqHash, err := s.findBase(sphs)
	if err != nil {
		return err
	}

	log.Printf("diffing %s { %s-%s -> %s-%s }",
		res.reqName[:res.matchLen],
		res.baseHash.String()[:5],
		res.baseName[res.matchLen:],
		reqHash.String()[:5],
		res.reqName[res.matchLen:],
	)

	baseEntries, err := s.getDigestsFromImage(res.baseHash)
	if err != nil {
		return err
	}
	reqEntries, err := s.getDigestsFromImage(reqHash)
	if err != nil {
		return err
	}

	dlen := int(s.cfg.Params.Params.DigestBits >> 3)
	cshift := blkshift(s.cfg.Params.Params.ChunkShift)
	baseIter := digestIterator{ents: baseEntries, digestLen: dlen, chunkShift: cshift}
	reqIter := digestIterator{ents: reqEntries, digestLen: dlen, chunkShift: cshift}

	var baseDigests, reqDigests []byte
	type info struct {
		size   int64
		slabId uint16
		addr   uint32
	}
	var baseInfo, reqInfo []info
	var baseTotalSize, reqTotalSize int64

	_ = s.db.View(func(tx *bbolt.Tx) error {
		db := tx.Bucket(chunkBucket)
		sb := tx.Bucket(slabBucket)

		has := func(d []byte) (uint16, uint32, bool) {
			loc := db.Get(d)
			if loc == nil {
				return 0, 0, false // shouldn't happen
			}
			slabId, addr := loadLoc(loc)
			return slabId, addr, sb.Bucket(slabKey(slabId)).Get(addrKey(addr|presentMask)) != nil
		}

		found := false
		for len(baseDigests) < dlen*s.cfg.ReadaheadChunks || len(reqDigests) < dlen*s.cfg.ReadaheadChunks {
			_, baseDigest, baseSize, baseOk := baseIter.next()
			_, reqDigest, reqSize, reqOk := reqIter.next()
			if !baseOk && !reqOk {
				break
			}

			// TODO: this algorithm is kind of awful

			// loop until we found the target digest
			found = found || bytes.Equal(reqDigest, targetDigest)
			if !found {
				continue
			}

			if baseOk && len(baseDigests) < dlen*s.cfg.ReadaheadChunks {
				baseSlab, baseAddr, baseHas := has(baseDigest)
				if baseHas {
					baseDigests = append(baseDigests, baseDigest...)
					baseInfo = append(baseInfo, info{baseSize, baseSlab, baseAddr})
					baseTotalSize += baseSize
					// log.Println("appending base", common.DigestStr(baseDigest), baseSlab, baseAddr, baseSize)
				}
			}
			if reqOk && len(reqDigests) < dlen*s.cfg.ReadaheadChunks {
				reqSlab, reqAddr, reqHas := has(reqDigest)
				if !reqHas {
					reqDigests = append(reqDigests, reqDigest...)
					reqInfo = append(reqInfo, info{reqSize, reqSlab, reqAddr})
					reqTotalSize += reqSize
					// log.Println("appending req", common.DigestStr(reqDigest), reqSlab, reqAddr, reqSize)
				}
			}
		}

		return nil
	})

	diff, err := s.getChunkDiff(ctx, baseDigests, reqDigests)
	if err != nil {
		log.Println("chunk diff failed", err)
		return s.readSingle(ctx, slabId, addr, targetDigest)
	}
	defer diff.Close()

	baseData := make([]byte, baseTotalSize)
	p := int64(0)
	for _, i := range baseInfo {
		if err := s.getKnownChunk(i.slabId, i.addr, baseData[p:p+i.size]); err != nil {
			return err
		}
		p += i.size
	}
	// log.Println("read baseData", len(baseData))

	// decompress from diff
	reqData, err := s.expandChunkDiff(ctx, baseData, diff)
	if err != nil {
		return err
	}

	// write out to slab
	p = 0
	for idx, i := range reqInfo {
		// slice with cap to force copy if less than block size
		b := reqData[p : p+i.size : p+i.size]
		digest := reqDigests[idx*dlen : (idx+1)*dlen]
		if err := s.gotNewChunk(i.slabId, i.addr, digest, b); err != nil {
			return err
		}
		p += i.size
	}

	// rest is json stats
	var diffStats manifester.ChunkDiffStats
	if err = json.Unmarshal(reqData[p:], &diffStats); err == nil {
		log.Printf("diff stats: %#v", diffStats)
	}

	return nil
}

func (s *server) expandChunkDiff(ctx context.Context, baseData []byte, diff io.Reader) ([]byte, error) {
	// run zstd
	// requires file for base, but diff can be streaming
	// TODO: this really should be in-process

	baseFile, err := writeToTempFile(baseData)
	if err != nil {
		return nil, err
	}
	defer os.Remove(baseFile)

	zstd := exec.CommandContext(
		ctx,
		common.ZstdBin,
		"-c",                     // stdout
		"-d",                     // decode
		"--patch-from", baseFile, // base
	)
	zstd.Stdin = diff
	out, err := zstd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return nil, fmt.Errorf("zstd error: %w: %q", err, stderr)
	}

	return out, nil
}

func (s *server) findBase(sphs []Sph) (catalogResult, Sph, error) {
	// find a base with similar data. go backwards on the assumption that recent images with
	// this chunk will be more similar.
	errs := []string{"couldn't find base"}
	for i := len(sphs) - 1; i >= 0; i-- {
		if res, err := s.catalog.findBase(sphs[i]); err == nil {
			return res, sphs[i], nil
		} else {
			errs = append(errs, err.Error())
		}
	}
	return catalogResult{}, Sph{}, errors.New(strings.Join(errs, "; "))
}

// gotNewChunk may reslice b up to block size and zero up to the new size!
func (s *server) gotNewChunk(slabId uint16, addr uint32, digest []byte, b []byte) error {
	if err := checkChunkDigest(b, digest); err != nil {
		return err
	}

	var fd int
	s.lock.Lock()
	if state := s.stateBySlab[slabId]; state != nil {
		fd = int(state.fd)
	}
	s.lock.Unlock()
	if fd == 0 {
		return errors.New("slab not loaded or missing fd")
	}

	// we can only write full blocks
	prevLen := len(b)
	rounded := int(s.blockShift.roundup(int64(prevLen)))
	if rounded > cap(b) {
		// need to copy
		buf := s.chunkPool.Get().([]byte)
		defer s.chunkPool.Put(buf)
		copy(buf, b)
		b = buf[:rounded]
	} else if rounded > prevLen {
		// can just reslice
		b = b[:rounded]
	}
	// zero padding in case our buffer was dirty
	for i := prevLen; i < rounded; i++ {
		b[i] = 0
	}

	// TODO: pass this all the way through?
	// if len(toWrite) < ln {
	// 	return fmt.Errorf("chunk underflowed requested len: %d < %d", len(toWrite), ln)
	// }

	off := int64(addr) << s.blockShift
	if n, err := unix.Pwrite(fd, b, off); err != nil {
		return err
	} else if n != len(b) {
		return fmt.Errorf("short write %d != %d", n, len(b))
	}

	// record async
	go s.db.Batch(func(tx *bbolt.Tx) error {
		sb := tx.Bucket(slabBucket).Bucket(slabKey(slabId))
		if sb == nil {
			return errors.New("missing slab bucket")
		}
		return sb.Put(addrKey(presentMask|addr), []byte{})
	})

	return nil
}

func (s *server) getChunkDiff(ctx context.Context, bases, reqs []byte) (io.ReadCloser, error) {
	reqBytes, err := json.Marshal(manifester.ChunkDiffReq{Bases: bases, Reqs: reqs})
	if err != nil {
		return nil, err
	}
	u := strings.TrimSuffix(s.cfg.Params.ChunkDiffUrl, "/") + manifester.ChunkDiffPath
	// TODO: use context here
	res, err := http.Post(u, "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	} else if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("chunk diff http status: %s", res.Status)
	}
	return res.Body, nil
}

func (s *server) getDigestsFromImage(sph Sph) ([]*pb.Entry, error) {
	var manifest pb.Manifest
	err := s.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(manifestBucket).Get([]byte(sph.String()))
		if v == nil {
			return errors.New("manifest not found")
		}
		return proto.Unmarshal(v, &manifest)
	})
	return valOrErr(manifest.Entries, err)
}

func (s *server) getKnownChunk(slabId uint16, addr uint32, buf []byte) error {
	var fd int
	s.lock.Lock()
	if state := s.stateBySlab[slabId]; state != nil {
		fd = int(state.cacheFd)
	}
	s.lock.Unlock()
	if fd == 0 {
		return errors.New("slab not loaded or missing cache")
	}

	_, err := unix.Pread(fd, buf, int64(addr)<<s.blockShift)
	return err
}

func (i *digestIterator) next() (string, []byte, int64, bool) {
	for {
		if i.e >= len(i.ents) {
			return "", nil, 0, false
		}
		ent := i.ents[i.e]
		if i.d >= len(ent.Digests) {
			i.e++
			i.d = 0
			continue
		}
		i.d += i.digestLen
		size := i.chunkShift.size()
		if i.d >= len(ent.Digests) { // last chunk
			size = i.chunkShift.leftover(ent.Size)
		}
		return ent.Path, ent.Digests[i.d-i.digestLen : i.d], size, true
	}
}
