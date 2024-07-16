package daemon

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"unsafe"

	"github.com/DataDog/zstd"
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
		e          int // _current_ index in ents
		d          int // _next_ digest offset
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
		diffRecompress              []string

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

func (s *server) requestChunk(ctx context.Context, loc erofs.SlabLoc, digest []byte, sphs []Sph, forceSingle bool) error {
	if _, ok := s.readKnownMap.Get(loc); ok {
		// We think we have this chunk and are trying to use it as a base, but we got asked for
		// it again. This shouldn't happen, but at least try to recover by doing a single read
		// instead of diffing more.
		log.Printf("bug: got request for supposedly-known chunk %s at %v", common.DigestStr(digest), loc)
		forceSingle = true
	}

	var op *diffOp

	s.diffLock.Lock()
	if haveOp, ok := s.diffMap[loc]; ok {
		op = haveOp
	} else if forceSingle {
		op, _ = s.buildSingleOp(ctx, loc, digest)
		go s.startOp(ctx, op)
	} else {
		var err error
		op, err = s.buildDiffOp(ctx, digest, sphs)
		if err != nil {
			op, err = s.buildSingleOp(ctx, loc, digest)
		} else if s.diffMap[loc] == nil {
			// shouldn't happen:
			log.Print("buildDiffOp did not include requested chunk")
			op, err = s.buildSingleOp(ctx, loc, digest)
		}
		go s.startOp(ctx, op)
	}
	s.diffLock.Unlock()

	// TODO: consider racing the diff against a single chunk read (with small delay)
	// return when either is done

	<-op.done

	if op.err != nil && op.tp != opTypeSingle {
		log.Printf("diff failed (%v), doing plain read", op.err)
		return s.requestChunk(ctx, loc, digest, sphs, true)
	}

	return op.err
}

// currently this is only used to read manifest chunks
func (s *server) readChunks(
	ctx context.Context,
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
		err := s.requestChunk(ctx, locs[firstMissing], digest, sphs, false)
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

	if err = s.gotNewChunk(loc, digest, chunk); err != nil {
		return fmt.Errorf("gotNewChunk error (single): %w", err)
	}
	return nil
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
		baseEntries, err := s.getDigestsFromImage(ctx, tx, res.baseHash, isManifest)
		if err != nil {
			log.Println("failed to get digests for", res.baseHash, res.baseName)
			return nil, err
		}
		baseIter = s.newDigestIterator(baseEntries)
	}
	reqEntries, err := s.getDigestsFromImage(ctx, tx, reqHash, isManifest)
	if err != nil {
		log.Println("failed to get digests for", reqHash, res.reqName)
		return nil, err
	}
	reqIter := s.newDigestIterator(reqEntries)

	op := newDiffOp(opTypeDiff)

	// build diff

	// find entry
	reqIdx := 0
	for {
		ok := reqIter.next(1)
		reqIdx++
		if !ok {
			// shouldn't happen
			return nil, fmt.Errorf("req digest not found in manifest")
		}
		if bytes.Equal(reqIter.digest(), targetDigest) {
			break
		}
	}

	reqEnt := reqIter.ent() // must not be nil here
	expandManPage := isManPageGz(reqEnt)
	expandLinuxKo := isLinuxKoXz(reqEnt)

	if expandManPage || expandLinuxKo {
		// diff with expanding and recompression

		switch {
		case expandManPage:
			// single file with recompression
			op.diffRecompress = []string{manifester.ExpandGz}
		case expandLinuxKo:
			// note: currently largest kernel module on my system (excluding kheaders) is
			// amdgpu.ko.xz at 3,538,004 bytes, 54 chunks (64kb), and expands to 25,589,856 bytes,
			// which is reasonable to pass through the chunk differ.
			// TODO: maybe get these args from looking at the base? or the chunk differ can look at
			// req and return them? or try several values and take the matching one?
			op.diffRecompress = []string{manifester.ExpandXz, "--check=crc32", "--lzma2=dict=1MiB"}
		}

		for i := 0; i < len(reqEnt.Digests); i += s.digestBytes {
			reqDigest := reqEnt.Digests[i : i+s.digestBytes]
			reqLoc, _ := s.digestPresent(tx, reqDigest)
			if reqLoc.Addr == 0 {
				// FIXME: shouldn't happen
			}
			// FIXME: figure out size
			op.addReq(reqDigest, reqSize, reqLoc)
			// FIXME: what if loc is already in diffMap :(
			s.diffMap[loc] = op
		}

		if usingBase {
			if baseIter.findFile(reqEnt.Path) {
				baseEnt := baseIter.ent()
				// FIXME: do same as loop above for base
			} else {
				log.Println("missing corresponding file", reqEnt.Path, "in", res.baseHash.String()[:5], res.baseName)
			}
		}
	} else {
		// normal diff

		// TODO: this algorithm is kind of awful

		// position baseIter at approximately the same place
		baseIter.next(reqIdx)

		maxDigestLen := s.digestBytes * s.cfg.ReadaheadChunks
		for len(op.baseDigests) < maxDigestLen || len(op.reqDigests) < maxDigestLen {
			baseDigest := baseIter.digest()
			reqDigest := reqIter.digest()
			if baseDigest != nil && len(op.baseDigests) < maxDigestLen {
				if baseLoc, basePresent := s.digestPresent(tx, baseDigest); basePresent {
					op.addBase(baseDigest, baseSize, baseLoc)
				}
			}
			if reqDigest != nil && len(op.reqDigests) < maxDigestLen {
				if reqLoc, reqPresent := s.digestPresent(tx, reqDigest); !reqPresent && reqLoc.Addr > 0 && s.diffMap[reqLoc] == nil {
					op.addReq(reqDigest, reqSize, reqLoc)
					// record we're diffing this one in the map
					s.diffMap[reqLoc] = op
				}
			}
			baseOk := baseIter.next()
			reqOk := reqIter.next()
			if !baseOk && !reqOk {
				break
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
	diff, err := s.getChunkDiff(ctx, op.baseDigests, op.reqDigests, op.diffRecompress)
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

	// decompress if needed
	baseData, err = s.diffDecompress(ctx, baseData, op.diffRecompress)
	if err != nil {
		return fmt.Errorf("decompress error: %w", err)
	}

	// decompress from diff
	reqData, err := io.ReadAll(zstd.NewReaderPatcher(diff, baseData))
	if err != nil {
		return fmt.Errorf("expandChunkDiff error: %w", err)
	}
	if len(reqData) < int(op.reqTotalSize) {
		return fmt.Errorf("decompressed data is too short: %d < %d", len(reqData), op.reqTotalSize)
	}

	// recompress if needed
	reqData, err = s.diffRecompress(ctx, reqData, op.diffRecompress)
	if err != nil {
		return fmt.Errorf("recompress error: %w", err)
	}

	// write out to slab
	p = 0
	for idx, i := range op.reqInfo {
		// slice with cap to force copy if less than block size
		b := reqData[p : p+i.size : p+i.size]
		digest := op.reqDigests[idx*s.digestBytes : (idx+1)*s.digestBytes]
		if err := s.gotNewChunk(i.loc, digest, b); err != nil {
			if len(op.diffRecompress) > 0 && strings.Contains(err.Error(), "digest mismatch") {
				// we didn't recompress correctly, fall back to single
				return fmt.Errorf("recompress mismatch")
			}
			return fmt.Errorf("gotNewChunk error (diff): %w", err)
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
	s.stateLock.Lock()
	if state := s.stateBySlab[loc.SlabId]; state != nil {
		writeFd = int(state.writeFd)
	}
	s.stateLock.Unlock()
	if writeFd == 0 {
		return errors.New("slab not loaded or missing write fd")
	}

	// we can only write full + aligned blocks
	prevLen := len(b)
	rounded := int(s.blockShift.Roundup(int64(prevLen)))
	bp := int64(uintptr(unsafe.Pointer(&b[0])))
	if rounded > cap(b) || s.blockShift.Leftover(bp) != 0 {
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
	s.presentMap.Put(loc, struct{}{})

	go func() {
		err := s.db.Batch(func(tx *bbolt.Tx) error {
			sb := tx.Bucket(slabBucket).Bucket(slabKey(loc.SlabId))
			if sb == nil {
				return errors.New("missing slab bucket")
			}
			return sb.Put(addrKey(presentMask|loc.Addr), []byte{})
		})
		if err == nil {
			s.presentMap.Del(loc)
		}
	}()

	return nil
}

func (s *server) getChunkDiff(ctx context.Context, bases, reqs []byte, recompress []string) (io.ReadCloser, error) {
	r := manifester.ChunkDiffReq{Bases: bases, Reqs: reqs}
	if len(recompress) > 0 {
		r.ExpandBeforeDiff = recompress[0]
	}
	reqBytes, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	u := strings.TrimSuffix(s.cfg.Params.ChunkDiffUrl, "/") + manifester.ChunkDiffPath
	res, err := retryHttpRequest(ctx, http.MethodPost, u, "application/json", reqBytes)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// note: called with diffLock and read-only tx
func (s *server) getDigestsFromImage(ctx context.Context, tx *bbolt.Tx, sph Sph, isManifest bool) ([]*pb.Entry, error) {
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
		data, err = s.readChunks(ctx, tx, entry.Size, locs, nil, nil, false)
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
func (s *server) getManifestLocal(ctx context.Context, tx *bbolt.Tx, key []byte) (*pb.Manifest, error) {
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
		data, err = s.readChunks(ctx, tx, entry.Size, locs, nil, nil, false)
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
	s.stateLock.Lock()
	if state := s.stateBySlab[loc.SlabId]; state != nil {
		readFd = int(state.readFd)
	}
	s.stateLock.Unlock()
	if readFd == 0 {
		return errors.New("slab not loaded or missing read fd")
	}

	// record that we're reading this out of the slab
	// TODO: use a real refcount
	if s.readKnownMap.PutIfNotPresent(loc, struct{}{}) {
		defer s.readKnownMap.Del(loc)
	}

	_, err := unix.Pread(readFd, buf, int64(loc.Addr)<<s.blockShift)
	return err
}

func (s *server) diffDecompress(ctx context.Context, data []byte, args []string) ([]byte, error) {
	if len(args) == 0 {
		return data, nil
	}
	switch args[0] {
	case manifester.ExpandGz:
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(gz)

	case manifester.ExpandXz:
		xz := exec.Command(common.XzBin, "-d")
		xz.Stdin = bytes.NewReader(data)
		return xz.Output()

	default:
		return nil, fmt.Errorf("unknown expander %q", args[0])
	}
}

func (s *server) diffRecompress(ctx context.Context, data []byte, args []string) ([]byte, error) {
	if len(args) == 0 {
		return data, nil
	}
	switch args[0] {
	case manifester.ExpandGz:
		gz := exec.Command(common.GzipBin, "-nc")
		gz.Stdin = bytes.NewReader(data)
		return gz.Output()

	case manifester.ExpandXz:
		xz := exec.Command(common.XzBin, append([]string{"-c"}, args[1:]...)...)
		xz.Stdin = bytes.NewReader(data)
		return xz.Output()

	default:
		return nil, fmt.Errorf("unknown expander %q", args[0])
	}
}

func (s *server) locPresent(tx *bbolt.Tx, loc erofs.SlabLoc) bool {
	if _, ok := s.presentMap.Get(loc); ok {
		return true
	}
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

func (i *digestIterator) ent() *pb.Entry {
	if i.e >= len(i.ents) {
		return nil
	}
	return i.ents[i.e]
}

func (i *digestIterator) digest() []byte {
	ent := i.ent()
	if ent == nil {
		return nil
	}
	if i.d > len(ent.Digests) {
		// shouldn't happen, we shouldn't have stopped here
		return nil
	}
	return ent.Digests[i.d-i.digestLen : i.d]
}

func (i *digestIterator) size() int64 {
	ent := i.ent()
	if ent == nil {
		return nil
	}
	if i.d >= len(ent.Digests) { // last chunk
		return i.chunkShift.Leftover(ent.Size)
	}
	return i.chunkShift.Size()
}

func (i *digestIterator) next(n int) bool {
	// FIXME: implement jump by n more efficiently
	for {
		ent := i.ent()
		if ent == nil {
			return false
		}
		if i.d >= len(ent.Digests) {
			i.e++
			i.d = 0
			continue
		}
		i.d += i.digestLen
		n--
		if n == 0 {
			return true
		}
	}
}

func (i *digestIterator) toFileStart() bool {
	ent := i.ent()
	if ent == nil {
		return false
	}
	i.d = i.digestLen
	return i.d <= len(ent.Digests)
}

func (i *digestIterator) findFile(path string) bool {
	for {
		ent := i.ent()
		if ent == nil {
			return false
		}
		if ent.Path == path {
			return i.toFileStart()
		}
		e++
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

var reManPage = regexp.MustCompile(`^/share/man/.*[.]gz$`)
var reLinuxKoXz = regexp.MustCompile(`^/lib/modules/[^/]+/kernel/.*[.]ko[.]xz$`)

func isManPageGz(ent *pb.Entry) bool {
	return ent.Type == pb.EntryType_REGULAR && reManPage.MatchString(ent.Path)
}
func isLinuxKoXz(ent *pb.Entry) bool {
	// kheaders.ko.xz is mostly an embedded .tar.xz file (yes, again), so expanding it won't help.
	return ent.Type == pb.EntryType_REGULAR && reLinuxKoXz.MatchString(ent.Path) &&
		path.Base(ent.Path) != "kheaders.ko.xz"
}
