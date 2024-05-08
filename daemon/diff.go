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
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

type (
	digestIterator struct {
		ents       []*pb.Entry
		digestLen  int
		chunkShift common.BlkShift
		e          int
		d          int
	}

	opType int

	diffOp struct {
		tp   opType        // type of operation. constant.
		err  error         // result. only written by startOp, read by callers after done is closed
		done chan struct{} // closed by startOp after writing err

		// info for diff op
		baseDigests, reqDigests     []byte
		baseInfo, reqInfo           []info
		baseTotalSize, reqTotalSize int64

		// info for single op
		singleLoc    erofs.SlabLoc
		singleDigest []byte
	}

	info struct {
		size int64
		loc  erofs.SlabLoc
	}
)

const (
	noBaseName = "<none>"

	opTypeDiff opType = iota
	opTypeSingle
)

func (s *server) requestChunk(loc erofs.SlabLoc, digest []byte, sphs []Sph, forceSingle bool) error {
	ctx := context.Background()

	s.readKnownLock.Lock()
	if _, ok := s.readKnownMap[loc]; ok {
		// We think we have this chunk and are trying to use it as a base, but we got asked for
		// it again. This shouldn't happen, but at least try to recover by doing a single read
		// instead of diffing more.
		log.Printf("bug: got request for supposedly-known chunk %s at %v", common.DigestStr(digest), loc)
		forceSingle = true
	}
	s.readKnownLock.Unlock()

	var op *diffOp

	s.diffLock.Lock()
	if haveOp, ok := s.diffMap[loc]; ok {
		op = haveOp
	} else if forceSingle {
		op, _ = s.buildSingleOp(ctx, loc, digest)
	} else {
		// build it
		var err error
		op, err = s.buildDiffOp(ctx, digest, sphs)
		if err != nil {
			op, err = s.buildSingleOp(ctx, loc, digest)
		} else if s.diffMap[loc] == nil {
			// shouldn't happen:
			log.Print("buildDiffOp did not include requested chunk")
			op, err = s.buildSingleOp(ctx, loc, digest)
		}
		// start it
		go s.startOp(ctx, op)
	}
	s.diffLock.Unlock()

	// TODO: consider racing the diff against a single chunk read (with small delay)
	// return when either is done

	<-op.done

	if op.err != nil && op.tp != opTypeSingle {
		log.Printf("diff failed (%v), doing plain read", op.err)
		return s.requestChunk(loc, digest, sphs, true)
	}

	return op.err
}

// currently this is only used to read manifest chunks
func (s *server) readChunks(
	useTx *bbolt.Tx, // optional
	totalSize int64,
	locs []erofs.SlabLoc,
	digests []byte, // used if allowMissing is true
	sphs []Sph, // used if allowMissing is true
	allowMissing bool,
) ([]byte, error) {
	firstMissing := -1
	findMissing := func(tx *bbolt.Tx) error {
		for idx, loc := range locs {
			if !s.locPresent(tx, loc) {
				firstMissing = idx
				return nil
			}
		}
		firstMissing = -1
		return nil
	}

	for {
		if useTx != nil {
			findMissing(useTx)
		} else {
			s.db.View(findMissing)
		}
		if firstMissing == -1 {
			break // we have them all
		}
		if !allowMissing {
			return nil, errors.New("there were missing chunks")
		}

		// request first missing one. the differ will do some readahead.
		digest := digests[firstMissing*s.digestBytes : (firstMissing+1)*s.digestBytes]
		err := s.requestChunk(locs[firstMissing], digest, sphs, false)
		if err != nil {
			return nil, err
		}
	}

	// read all from slabs. all but last chunk must be full.
	out := make([]byte, totalSize)
	rest := out
	for _, loc := range locs {
		toRead := min(1<<s.cfg.Params.Params.ChunkShift, len(rest))
		err := s.getKnownChunk(loc, rest[:toRead])
		if err != nil {
			return nil, err
		}
		rest = rest[toRead:]
	}
	return out, nil
}

func (s *server) readSingle(ctx context.Context, loc erofs.SlabLoc, digest []byte) error {
	// we have no size info here
	buf := s.chunkPool.Get(1 << s.cfg.Params.Params.ChunkShift)
	defer s.chunkPool.Put(buf)

	digestStr := common.DigestStr(digest)
	chunk, err := s.csread.Get(ctx, digestStr, buf[:0])
	if err != nil {
		return fmt.Errorf("chunk read error: %w", err)
	} else if len(chunk) > len(buf) || &buf[0] != &chunk[0] {
		return fmt.Errorf("chunk overflowed chunk size: %d > %d", len(chunk), len(buf))
	}

	return s.gotNewChunk(loc, digest, chunk)
}

// call with diffLock held
func (s *server) buildDiffOp(
	ctx context.Context,
	targetDigest []byte,
	sphs []Sph,
) (*diffOp, error) {
	// TODO: able to use multiple bases at once
	res, reqHash := s.findBase(sphs)
	usingBase := res.baseName != noBaseName

	isManifest := strings.HasPrefix(res.reqName, isManifestPrefix)
	if usingBase {
		if isManifest != strings.HasPrefix(res.baseName, isManifestPrefix) {
			panic("catalog should not match manifest with data")
		}
	}

	tx, err := s.db.Begin(false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var baseIter digestIterator
	if usingBase {
		baseEntries, err := s.getDigestsFromImage(tx, res.baseHash, isManifest)
		if err != nil {
			log.Println("failed to get digests for", res.baseHash, res.baseName)
			return nil, err
		}
		baseIter = s.newDigestIterator(baseEntries)
	}
	reqEntries, err := s.getDigestsFromImage(tx, reqHash, isManifest)
	if err != nil {
		log.Println("failed to get digests for", reqHash, res.reqName)
		return nil, err
	}
	reqIter := s.newDigestIterator(reqEntries)

	op := newDiffOp(opTypeDiff)

	// build diff
	// TODO: this algorithm is kind of awful

	found := false
	maxDigestLen := s.digestBytes * s.cfg.ReadaheadChunks
	for len(op.baseDigests) < maxDigestLen || len(op.reqDigests) < maxDigestLen {
		_, baseDigest, baseSize, baseOk := baseIter.next()
		_, reqDigest, reqSize, reqOk := reqIter.next()
		if !baseOk && !reqOk {
			break
		}

		// loop until we found the target digest
		found = found || bytes.Equal(reqDigest, targetDigest)
		if !found {
			continue
		}

		if baseOk && len(op.baseDigests) < maxDigestLen {
			if baseLoc, basePresent := s.digestPresent(tx, baseDigest); basePresent {
				op.addBase(baseDigest, baseSize, baseLoc)
			}
		}
		if reqOk && len(op.reqDigests) < maxDigestLen {
			if reqLoc, reqPresent := s.digestPresent(tx, reqDigest); !reqPresent && s.diffMap[reqLoc] == nil {
				op.addReq(reqDigest, reqSize, reqLoc)
				// record we're diffing this one in the map
				s.diffMap[reqLoc] = op
			}
		}
	}

	if usingBase {
		log.Printf("diffing %s…-%s -> %s…-%s [%d/%d -> %d/%d]",
			res.baseHash.String()[:5],
			res.baseName,
			reqHash.String()[:5],
			res.reqName,
			op.baseTotalSize,
			len(op.baseInfo),
			op.reqTotalSize,
			len(op.reqInfo),
		)
	} else {
		log.Printf("requesting %s…-%s [%d/%d]",
			reqHash.String()[:5],
			res.reqName,
			op.reqTotalSize,
			len(op.reqInfo),
		)
	}

	return op, nil
}

// call with diffLock held
func (s *server) buildSingleOp(
	ctx context.Context,
	loc erofs.SlabLoc,
	targetDigest []byte,
) (*diffOp, error) {
	op := newDiffOp(opTypeSingle)
	op.singleLoc = loc
	op.singleDigest = targetDigest
	s.diffMap[loc] = op
	return op, nil
}

// runs in separate goroutine
func (s *server) startOp(ctx context.Context, op *diffOp) {
	defer func() {
		if r := recover(); r != nil {
			op.err = fmt.Errorf("panic in diff op: %v", r)
		}

		// clear references to this op from the map
		s.diffLock.Lock()
		switch op.tp {
		case opTypeDiff:
			for _, i := range op.reqInfo {
				delete(s.diffMap, i.loc)
			}
		case opTypeSingle:
			delete(s.diffMap, op.singleLoc)
		}
		s.diffLock.Unlock()

		// wake up waiters
		close(op.done)
	}()

	switch op.tp {
	case opTypeDiff:
		if len(op.baseInfo) == 0 {
			s.stats.batchReqs.Add(1)
		} else {
			s.stats.diffReqs.Add(1)
		}
		op.err = s.doDiffOp(ctx, op)
		if op.err != nil {
			if len(op.baseInfo) == 0 {
				s.stats.batchErrs.Add(1)
			} else {
				s.stats.diffErrs.Add(1)
			}
		}
	case opTypeSingle:
		s.stats.singleReqs.Add(1)
		op.err = s.readSingle(ctx, op.singleLoc, op.singleDigest)
		if op.err != nil {
			s.stats.singleErrs.Add(1)
		}
	}
}

func (s *server) doDiffOp(ctx context.Context, op *diffOp) error {
	diff, err := s.getChunkDiff(ctx, op.baseDigests, op.reqDigests)
	if err != nil {
		return fmt.Errorf("getChunkDiff error: %w", err)
	}
	defer diff.Close()

	baseData := make([]byte, op.baseTotalSize)
	p := int64(0)
	for _, i := range op.baseInfo {
		if err := s.getKnownChunk(i.loc, baseData[p:p+i.size]); err != nil {
			return fmt.Errorf("getKnownChunk error: %w", err)
		}
		p += i.size
	}
	// log.Println("read baseData", len(baseData))

	// decompress from diff
	reqData, err := s.expandChunkDiff(ctx, baseData, diff)
	if err != nil {
		return fmt.Errorf("expandChunkDiff error: %w", err)
	}
	if len(reqData) < int(op.reqTotalSize) {
		return fmt.Errorf("decompressed data is too short: %d < %d", len(reqData), op.reqTotalSize)
	}

	// write out to slab
	p = 0
	for idx, i := range op.reqInfo {
		// slice with cap to force copy if less than block size
		b := reqData[p : p+i.size : p+i.size]
		digest := op.reqDigests[idx*s.digestBytes : (idx+1)*s.digestBytes]
		if err := s.gotNewChunk(i.loc, digest, b); err != nil {
			return fmt.Errorf("gotNewChunk error: %w", err)
		}
		p += i.size
	}

	// rest is json stats
	var st manifester.ChunkDiffStats
	if err = json.Unmarshal(reqData[p:], &st); err == nil {
		log.Printf("diff %d/%d -> %d/%d = %d (%.1f%%)",
			st.BaseBytes, st.BaseChunks, st.ReqBytes, st.ReqChunks,
			st.DiffBytes, 100*float64(st.DiffBytes)/float64(st.ReqBytes))
	}

	return nil
}

func (s *server) expandChunkDiff(ctx context.Context, baseData []byte, diff io.Reader) ([]byte, error) {
	// run zstd
	// requires file for base, but diff can be streaming
	// TODO: this really should be in-process

	baseFile, err := writeToTempFile(baseData)
	if err != nil {
		return nil, fmt.Errorf("writeToTempFile error: %w", err)
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

func (s *server) findBase(sphs []Sph) (catalogResult, Sph) {
	// find an image with a base with similar data. go backwards on the assumption that recent
	// images with this chunk will be more similar.
	for i := len(sphs) - 1; i >= 0; i-- {
		if res, err := s.catalog.findBase(sphs[i]); err == nil {
			return res, sphs[i]
		}
	}
	// can't find any base, diff latest against nothing
	sph := sphs[len(sphs)-1]
	name := s.catalog.findName(sph)
	return catalogResult{
		reqName:  name,
		baseName: noBaseName,
	}, sph
}

// gotNewChunk may reslice b up to block size and zero up to the new size!
func (s *server) gotNewChunk(loc erofs.SlabLoc, digest []byte, b []byte) error {
	if err := checkChunkDigest(b, digest); err != nil {
		return err
	}

	var writeFd int
	s.lock.Lock()
	if state := s.stateBySlab[loc.SlabId]; state != nil {
		writeFd = int(state.writeFd)
	}
	s.lock.Unlock()
	if writeFd == 0 {
		return errors.New("gotNewChunk: slab not loaded or missing write fd")
	}

	// we can only write full blocks
	prevLen := len(b)
	rounded := int(s.blockShift.Roundup(int64(prevLen)))
	if rounded > cap(b) {
		// need to copy
		buf := s.chunkPool.Get(rounded)
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

	off := int64(loc.Addr) << s.blockShift
	if n, err := unix.Pwrite(writeFd, b, off); err != nil {
		return fmt.Errorf("pwrite error: %w", err)
	} else if n != len(b) {
		return fmt.Errorf("short write %d != %d", n, len(b))
	}

	// record async
	s.presentLock.Lock()
	s.presentMap[loc] = struct{}{}
	s.presentLock.Unlock()

	go func() {
		err := s.db.Batch(func(tx *bbolt.Tx) error {
			sb := tx.Bucket(slabBucket).Bucket(slabKey(loc.SlabId))
			if sb == nil {
				return errors.New("missing slab bucket")
			}
			return sb.Put(addrKey(presentMask|loc.Addr), []byte{})
		})
		if err == nil {
			s.presentLock.Lock()
			delete(s.presentMap, loc)
			s.presentLock.Unlock()
		}
	}()

	return nil
}

func (s *server) getChunkDiff(ctx context.Context, bases, reqs []byte) (io.ReadCloser, error) {
	reqBytes, err := json.Marshal(manifester.ChunkDiffReq{Bases: bases, Reqs: reqs})
	if err != nil {
		return nil, err
	}
	u := strings.TrimSuffix(s.cfg.Params.ChunkDiffUrl, "/") + manifester.ChunkDiffPath
	// TODO: retries here
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	} else if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return nil, fmt.Errorf("chunk diff http status: %s", res.Status)
	}
	return res.Body, nil
}

// note: called with diffLock and read-only tx
func (s *server) getDigestsFromImage(tx *bbolt.Tx, sph Sph, isManifest bool) ([]*pb.Entry, error) {
	if isManifest {
		// get the image sph back. makeManifestSph is its own inverse.
		sph = makeManifestSph(sph)
	}

	v := tx.Bucket(manifestBucket).Get([]byte(sph.String()))
	if v == nil {
		return nil, errors.New("manifest not found")
	}
	var sm pb.SignedMessage
	err := proto.Unmarshal(v, &sm)
	if err != nil {
		return nil, err
	}

	entry := sm.Msg
	if isManifest {
		return []*pb.Entry{entry}, nil
	}

	// read chunks if needed
	data := entry.InlineData
	if len(data) == 0 {
		locs, err := s.lookupLocs(tx, entry.Digests)
		if err != nil {
			return nil, err
		}
		data, err = s.readChunks(tx, entry.Size, locs, nil, nil, false)
		if err != nil {
			return nil, err
		}
	}

	// unmarshal
	var m pb.Manifest
	err = proto.Unmarshal(data, &m)
	return common.ValOrErr(m.Entries, err)
}

// simplied form of getDigestsFromImage (TODO: consolidate)
func (s *server) getManifest(tx *bbolt.Tx, key []byte) (*pb.Manifest, error) {
	v := tx.Bucket(manifestBucket).Get(key)
	if v == nil {
		return nil, errors.New("manifest not found")
	}
	var sm pb.SignedMessage
	err := proto.Unmarshal(v, &sm)
	if err != nil {
		return nil, err
	}

	// read chunks if needed
	entry := sm.Msg
	data := entry.InlineData
	if len(data) == 0 {
		locs, err := s.lookupLocs(tx, entry.Digests)
		if err != nil {
			return nil, err
		}
		data, err = s.readChunks(tx, entry.Size, locs, nil, nil, false)
		if err != nil {
			return nil, err
		}
	}

	// unmarshal
	var m pb.Manifest
	err = proto.Unmarshal(data, &m)
	return common.ValOrErr(&m, err)
}

func (s *server) getKnownChunk(loc erofs.SlabLoc, buf []byte) error {
	var readFd int
	s.lock.Lock()
	if state := s.stateBySlab[loc.SlabId]; state != nil {
		readFd = int(state.readFd)
	}
	s.lock.Unlock()
	if readFd == 0 {
		return errors.New("slab not loaded or missing read fd")
	}

	// record that we're reading this out of the slab
	s.readKnownLock.Lock()
	s.readKnownMap[loc] = struct{}{}
	s.readKnownLock.Unlock()

	_, err := unix.Pread(readFd, buf, int64(loc.Addr)<<s.blockShift)

	s.readKnownLock.Lock()
	delete(s.readKnownMap, loc)
	s.readKnownLock.Unlock()

	return err
}

func (s *server) locPresent(tx *bbolt.Tx, loc erofs.SlabLoc) bool {
	s.presentLock.Lock()
	if _, ok := s.presentMap[loc]; ok {
		s.presentLock.Unlock()
		return true
	}
	s.presentLock.Unlock()

	sb := tx.Bucket(slabBucket)
	db := sb.Bucket(slabKey(loc.SlabId))
	return db.Get(addrKey(loc.Addr|presentMask)) != nil
}

func (s *server) digestPresent(tx *bbolt.Tx, digest []byte) (erofs.SlabLoc, bool) {
	v := tx.Bucket(chunkBucket).Get(digest)
	if v == nil {
		return erofs.SlabLoc{}, false // shouldn't happen
	}
	loc := loadLoc(v)
	return loc, s.locPresent(tx, loc)
}

func (s *server) newDigestIterator(entries []*pb.Entry) digestIterator {
	return digestIterator{ents: entries, digestLen: s.digestBytes, chunkShift: s.chunkShift}
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
		size := i.chunkShift.Size()
		if i.d >= len(ent.Digests) { // last chunk
			size = i.chunkShift.Leftover(ent.Size)
		}
		return ent.Path, ent.Digests[i.d-i.digestLen : i.d], size, true
	}
}

func newDiffOp(tp opType) *diffOp {
	return &diffOp{
		tp:   tp,
		done: make(chan struct{}),
	}
}

func (op *diffOp) addBase(digest []byte, size int64, loc erofs.SlabLoc) {
	op.baseDigests = append(op.baseDigests, digest...)
	op.baseInfo = append(op.baseInfo, info{size, loc})
	op.baseTotalSize += size
}

func (op *diffOp) addReq(digest []byte, size int64, loc erofs.SlabLoc) {
	op.reqDigests = append(op.reqDigests, digest...)
	op.reqInfo = append(op.reqInfo, info{size, loc})
	op.reqTotalSize += size
}
