package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"strings"
	"time"
	"unsafe"

	"github.com/DataDog/zstd"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

const (
	recentReadExpiry = 30 * time.Second

	// only public so they can be referenced by tests
	InitOpSize = 16
	MaxOpSize  = 128 // must be ≤ manifester.ChunkDiffMaxDigests
	MaxDiffOps = 8
	MaxSources = 3
)

type (
	digestIterator struct {
		ents []*pb.Entry
		e    int // current index in ents
		d    int // current digest offset (bytes)
	}

	reqOp interface {
		wait() error
	}

	singleOp struct {
		err  error         // result. only written by start, read by wait
		done chan struct{} // closed by start after writing err

		loc    erofs.SlabLoc
		digest cdig.CDig
	}

	diffOp struct {
		err  error         // result. only written by start, read by wait
		done chan struct{} // closed by start after writing err

		baseDigests, reqDigests     []cdig.CDig
		baseInfo, reqInfo           []info
		baseTotalSize, reqTotalSize int32
		recompress                  []string

		// shared with all ops in opSet.
		// the contents of the recentReads are under diffLock.
		rrs *[MaxSources]*recentRead
	}

	// context for building set of diff ops
	opSet struct {
		s     *Server
		tx    *bbolt.Tx
		op    *diffOp // last op in ops
		ops   []*diffOp
		using map[cdig.CDig]struct{}
		// rrs is indirect so that diffOps can point to it without keeping opSet live
		rrs         *[MaxSources]*recentRead
		limitShift  int
		maxOpSize   int
		maxOps      int
		sourcesLeft int
	}

	info struct {
		size int32
		loc  erofs.SlabLoc
	}

	recentRead struct {
		when  time.Time
		reads int
	}
)

func (s *Server) requestChunk(ctx context.Context, loc erofs.SlabLoc, digest cdig.CDig, sphps []SphPrefix) error {
	if _, ok := s.readKnownMap.Get(loc); ok {
		// We think we have this chunk and are trying to use it as a base, but we got asked for
		// it again. This shouldn't happen, but at least try to recover by doing a single read
		// instead of diffing more.
		log.Printf("bug: got request for supposedly-known chunk %s at %v", digest.String(), loc)
		sphps = nil
	}

	var op reqOp

	s.diffLock.Lock()
	if op = s.diffMap[loc]; op != nil {
		// being request already, wait on this one
	} else if len(sphps) == 0 {
		log.Print("missing sph references")
	} else {
		set := newOpSet(s)
		tx, err := s.db.Begin(false)
		if err != nil {
			return err
		}
		err = set.buildDiff(tx, digest, sphps, true)
		tx.Rollback()
		if err != nil {
			log.Printf("buildDiff failed: %v", err)
		} else if op = s.diffMap[loc]; op == nil {
			log.Print("buildDiff did not include requested chunk") // shouldn't happen
		} else {
			// TODO: if set is a single op, with a single req and no base, change to single

			// note that op is left as diffMap[loc] to wait on
			for _, startOp := range set.ops {
				go s.startDiffOp(ctx, startOp)
			}
			if extra := len(set.ops) - 1; extra > 0 {
				s.stats.extraReqs.Add(int64(extra))
			}
		}
	}
	if op == nil {
		sop := s.buildSingleOp(loc, digest)
		go s.startSingleOp(ctx, sop)
		op = sop
	}
	s.diffLock.Unlock()

	// TODO: consider racing the diff against a single chunk read (with small delay)
	// return when either is done

	err := op.wait()
	if err != nil {
		if _, ok := op.(*singleOp); !ok {
			log.Printf("diff failed (%v), doing plain read", err)
			return s.requestChunk(ctx, loc, digest, nil)
		}
	}

	return err
}

func (s *Server) requestPrefetch(ctx context.Context, reqs []cdig.CDig) error {
	ops, err := s.buildAndStartPrefetch(ctx, reqs)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := op.wait(); err != nil {
			log.Printf("prefetch request failed (%v)", err)
		}
	}
	for _, op := range ops {
		if err := op.wait(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) buildAndStartPrefetch(ctx context.Context, reqs []cdig.CDig) ([]reqOp, error) {
	s.diffLock.Lock()
	defer s.diffLock.Unlock()

	tx, err := s.db.Begin(false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	cb := tx.Bucket(chunkBucket)

	var allOps []reqOp
	have := make(map[reqOp]struct{})

	for _, req := range reqs {
		loc := cb.Get(req[:])
		if loc == nil {
			return nil, errors.New("missing digest->loc reference")
		}
		l := loadLoc(loc)
		if op := s.diffMap[l]; op != nil {
			// already being requested
			if _, ok := have[op]; !ok {
				have[op] = struct{}{}
				allOps = append(allOps, op)
			}
			continue
		} else if s.locPresent(tx, l) {
			continue
		}
		// build new requests
		sphps := splitSphs(loc[6:])
		if len(sphps) == 0 {
			return nil, errors.New("missing sph references")
		}
		set := newOpSet(s)
		set.maxOpSize = MaxOpSize // use larger ops immediately
		err := set.buildDiff(tx, req, sphps, false)
		if err != nil {
			return nil, err
		} else if op := s.diffMap[l]; op == nil {
			return nil, errors.New("buildDiff did not include requested chunk")
		} else {
			have[op] = struct{}{}
			allOps = append(allOps, op)
		}
		for _, startOp := range set.ops {
			go s.startDiffOp(ctx, startOp)
		}
		if extra := len(set.ops) - 1; extra > 0 {
			s.stats.extraReqs.Add(int64(extra))
		}
	}

	return allOps, nil
}

// currently this is only used to read manifest chunks
func (s *Server) readChunks(
	ctx context.Context,
	useTx *bbolt.Tx, // optional
	totalSize int64,
	locs []erofs.SlabLoc,
	digests []cdig.CDig, // used if allowMissing is true
	sphps []SphPrefix, // used if allowMissing is true
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
		err := s.requestChunk(ctx, locs[firstMissing], digests[firstMissing], sphps)
		if err != nil {
			return nil, err
		}
	}

	// read all from slabs. all but last chunk must be full.
	out := make([]byte, totalSize)
	rest := out
	for _, loc := range locs {
		toRead := min(int(common.ChunkShift.Size()), len(rest))
		err := s.getKnownChunk(loc, rest[:toRead])
		if err != nil {
			return nil, err
		}
		rest = rest[toRead:]
	}
	return out, nil
}

func (s *Server) readSingle(ctx context.Context, loc erofs.SlabLoc, digest cdig.CDig) error {
	// we have no size info here
	buf := s.chunkPool.Get(int(common.ChunkShift.Size()))
	defer s.chunkPool.Put(buf)

	chunk, err := s.p().csread.Get(ctx, digest.String(), buf[:0])
	if err != nil {
		return fmt.Errorf("chunk read error: %w", err)
	} else if len(chunk) > len(buf) || &buf[0] != &chunk[0] {
		return fmt.Errorf("chunk overflowed chunk size: %d > %d", len(chunk), len(buf))
	}
	s.stats.singleBytes.Add(int64(len(chunk)))

	if err = s.gotNewChunk(loc, digest, chunk); err != nil {
		return fmt.Errorf("gotNewChunk error (single): %w", err)
	}
	return nil
}

// call with diffLock held
func (s *Server) buildSingleOp(
	loc erofs.SlabLoc,
	targetDigest cdig.CDig,
) *singleOp {
	op := &singleOp{
		done:   make(chan struct{}),
		loc:    loc,
		digest: targetDigest,
	}
	s.diffMap[loc] = op
	return op
}

// runs in separate goroutine
func (s *Server) startSingleOp(ctx context.Context, op *singleOp) {
	defer func() {
		if r := recover(); r != nil {
			op.err = fmt.Errorf("panic in single op: %v", r)
		}
		if op.err != nil {
			s.stats.singleErrs.Add(1)
		}

		// clear references to this op from the map
		s.diffLock.Lock()
		if s.diffMap[op.loc] == reqOp(op) {
			delete(s.diffMap, op.loc)
		}
		s.diffLock.Unlock()

		// wake up waiters
		close(op.done)
	}()

	s.stats.singleReqs.Add(1)
	if op.err = s.diffSem.Acquire(ctx, 1); op.err == nil {
		defer s.diffSem.Release(1)
		op.err = s.readSingle(ctx, op.loc, op.digest)
	}
}

// runs in separate goroutine
func (s *Server) startDiffOp(ctx context.Context, op *diffOp) {
	defer func() {
		if r := recover(); r != nil {
			op.err = fmt.Errorf("panic in diff op: %v", r)
		}
		if op.err != nil {
			if !op.hasBase() {
				s.stats.batchErrs.Add(1)
			} else {
				s.stats.diffErrs.Add(1)
			}
		}

		// clear references to this op from the map
		s.diffLock.Lock()
		for _, i := range op.reqInfo {
			if s.diffMap[i.loc] == reqOp(op) {
				delete(s.diffMap, i.loc)
			}
		}
		// update recentRead timers
		for _, rr := range op.rrs[:] {
			if rr != nil {
				rr.when = time.Now()
			}
		}
		s.diffLock.Unlock()

		// wake up waiters
		close(op.done)
	}()

	if !op.hasBase() {
		s.stats.batchReqs.Add(1)
	} else {
		s.stats.diffReqs.Add(1)
	}
	if op.err = s.diffSem.Acquire(ctx, 1); op.err == nil {
		defer s.diffSem.Release(1)
		op.err = s.doDiffOp(ctx, op)
	}
}

func (s *Server) doDiffOp(ctx context.Context, op *diffOp) error {
	diff, err := s.getChunkDiff(ctx, op.baseDigests, op.reqDigests, op.recompress)
	if err != nil {
		return fmt.Errorf("getChunkDiff error: %w", err)
	}
	defer diff.Close()

	var p int64
	var baseData []byte

	if op.hasBase() {
		baseData = make([]byte, op.baseTotalSize)
		for _, i := range op.baseInfo {
			if err := s.getKnownChunk(i.loc, baseData[p:p+int64(i.size)]); err != nil {
				return fmt.Errorf("getKnownChunk error: %w", err)
			}
			p += int64(i.size)
		}

		// decompress if needed
		baseData, err = doDiffDecompress(ctx, baseData, op.recompress)
		if err != nil {
			return fmt.Errorf("decompress error: %w", err)
		}
	}

	// decompress from diff
	diffCounter := countReader{r: diff}
	reqData, err := io.ReadAll(zstd.NewReaderPatcher(&diffCounter, baseData))
	if err != nil {
		return fmt.Errorf("expandChunkDiff error: %w", err)
	}
	if !op.hasBase() {
		s.stats.batchBytes.Add(diffCounter.c)
	} else {
		s.stats.diffBytes.Add(diffCounter.c)
	}

	var statsBytes []byte
	if len(op.recompress) > 0 {
		// reqData contains the concatenation of _un_compressed data plus stats.
		// we need to recompress the data but not the stats, so strip off the stats.
		// note: this only works since stats are only ints. if we have nested objects or
		// strings we'll need a more complicated parser.
		statsStart := bytes.LastIndexByte(reqData, '{')
		if statsStart < 0 {
			return fmt.Errorf("diff data has bad stats")
		}
		statsBytes = reqData[statsStart:]
		reqData, err = doDiffRecompress(ctx, reqData[:statsStart], op.recompress)
		if err != nil {
			return fmt.Errorf("recompress error: %w", err)
		}
	}

	if len(reqData) < int(op.reqTotalSize) {
		return fmt.Errorf("decompressed data is too short: %d < %d", len(reqData), op.reqTotalSize)
	}

	// write out to slab
	p = 0
	for idx, i := range op.reqInfo {
		// slice with cap to force copy if less than block size
		b := reqData[p : p+int64(i.size) : p+int64(i.size)]
		if err := s.gotNewChunk(i.loc, op.reqDigests[idx], b); err != nil {
			if len(op.recompress) > 0 && strings.Contains(err.Error(), "digest mismatch") {
				// we didn't recompress correctly, fall back to single
				// TODO: be able to try with different parameter variants
				return fmt.Errorf("recompress mismatch")
			}
			return fmt.Errorf("gotNewChunk error (diff): %w", err)
		}
		p += int64(i.size)
	}

	// rest is json stats
	var st manifester.ChunkDiffStats
	if statsBytes == nil {
		// if we didn't recompress, stats follow immediately after data.
		statsBytes = reqData[p:]
	}
	if err = json.Unmarshal(statsBytes, &st); err == nil {
		if st.BaseChunks > 0 {
			log.Printf("diff [%d:%d <~ %d:%d] = %d (%.1f%%)",
				st.ReqChunks, st.ReqBytes, st.BaseChunks, st.BaseBytes,
				st.DiffBytes, 100*float64(st.DiffBytes)/float64(st.ReqBytes))
		} else {
			log.Printf("batch [%d:%d] = %d (%.1f%%)",
				st.ReqChunks, st.ReqBytes,
				st.DiffBytes, 100*float64(st.DiffBytes)/float64(st.ReqBytes))
		}
	} else {
		log.Println("diff data has bad stats", err)
	}

	return nil
}

// gotNewChunk may reslice b up to block size and zero up to the new size!
func (s *Server) gotNewChunk(loc erofs.SlabLoc, digest cdig.CDig, b []byte) error {
	if err := digest.Check(b); err != nil {
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

func (s *Server) getChunkDiff(ctx context.Context, bases, reqs []cdig.CDig, recompress []string) (io.ReadCloser, error) {
	r := manifester.ChunkDiffReq{Bases: cdig.ToSliceAlias(bases), Reqs: cdig.ToSliceAlias(reqs)}
	if len(recompress) > 0 {
		r.ExpandBeforeDiff = recompress[0]
	}
	reqBytes, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	u := strings.TrimSuffix(s.p().params.ChunkDiffUrl, "/") + manifester.ChunkDiffPath
	res, err := retryHttpRequest(ctx, http.MethodPost, u, "application/json", reqBytes)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// note: called with read-only tx
func (s *Server) getDigestsFromImage(ctx context.Context, tx *bbolt.Tx, sph Sph, isManifest bool) ([]*pb.Entry, error) {
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
		locs, err := s.lookupLocs(tx, cdig.FromSliceAlias(entry.Digests))
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

// simplified form of getDigestsFromImage (TODO: consolidate)
func (s *Server) getManifestLocal(ctx context.Context, tx *bbolt.Tx, key []byte) (*pb.Manifest, error) {
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
		locs, err := s.lookupLocs(tx, cdig.FromSliceAlias(entry.Digests))
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

func (s *Server) getKnownChunk(loc erofs.SlabLoc, buf []byte) error {
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

func (s *Server) locPresent(tx *bbolt.Tx, loc erofs.SlabLoc) bool {
	if _, ok := s.presentMap.Get(loc); ok {
		return true
	}
	sb := tx.Bucket(slabBucket)
	db := sb.Bucket(slabKey(loc.SlabId))
	return db.Get(addrKey(loc.Addr|presentMask)) != nil
}

func (s *Server) digestLoc(tx *bbolt.Tx, digest cdig.CDig) erofs.SlabLoc {
	v := tx.Bucket(chunkBucket).Get(digest[:])
	if v == nil {
		log.Println("missing chunk entry in digestLoc", digest)
		return erofs.SlabLoc{} // shouldn't happen
	}
	return loadLoc(v)
}

func (s *Server) digestPresent(tx *bbolt.Tx, digest cdig.CDig) (erofs.SlabLoc, bool) {
	loc := s.digestLoc(tx, digest)
	return loc, s.locPresent(tx, loc)
}

func (s *Server) findRecentRead(reqHash Sph, path string) *recentRead {
	key := string(reqHash[:]) + path
	if rr := s.recentReads[key]; rr != nil {
		rr.reads++
		// log.Printf("another read for %s, increasing request size", path)
		rr.when = time.Now()
		return rr
	}
	rr := &recentRead{when: time.Now()}
	s.recentReads[key] = rr
	return rr
}

func (s *Server) pruneRecentReads() {
	t := time.NewTicker(recentReadExpiry / 2)
	defer t.Stop()
	for range t.C {
		s.diffLock.Lock()
		now := time.Now()
		maps.DeleteFunc(s.recentReads, func(key string, rr *recentRead) bool {
			return now.Sub(rr.when) > recentReadExpiry
		})
		s.diffLock.Unlock()
	}
}

// single op

func (op *singleOp) wait() error {
	<-op.done
	return op.err
}

// diff op

func (op *diffOp) wait() error {
	<-op.done
	return op.err
}

func (op *diffOp) hasBase() bool {
	return len(op.baseInfo) > 0
}

func (op *diffOp) hasReq() bool {
	return len(op.reqInfo) > 0
}

func (op *diffOp) resetDiff() {
	op.baseDigests = nil
	op.reqDigests = nil
	op.baseInfo = nil
	op.reqInfo = nil
	op.baseTotalSize = 0
	op.reqTotalSize = 0
	op.recompress = nil
}

func (op *diffOp) addBase(digest cdig.CDig, size int32, loc erofs.SlabLoc) {
	op.baseDigests = append(op.baseDigests, digest)
	op.baseInfo = append(op.baseInfo, info{size, loc})
	op.baseTotalSize += size
}

func (op *diffOp) addReq(digest cdig.CDig, size int32, loc erofs.SlabLoc) {
	op.reqDigests = append(op.reqDigests, digest)
	op.reqInfo = append(op.reqInfo, info{size, loc})
	op.reqTotalSize += size
}

// op set

func newOpSet(s *Server) *opSet {
	set := &opSet{
		s:           s,
		using:       make(map[cdig.CDig]struct{}),
		rrs:         new([MaxSources]*recentRead),
		maxOpSize:   InitOpSize,
		maxOps:      1,
		sourcesLeft: MaxSources,
	}
	set.newOp()
	return set
}

func (set *opSet) shiftMax(limitShift int) {
	for set.limitShift < limitShift {
		set.limitShift++
		if set.maxOpSize < MaxOpSize {
			set.maxOpSize *= 2
		} else if set.maxOps < MaxDiffOps {
			set.maxOps *= 2
		}
	}
}

func (set *opSet) addRecentRead(rr *recentRead) {
	for i := 0; i < MaxSources; i++ {
		if set.rrs[i] == rr {
			return
		} else if set.rrs[i] == nil {
			set.rrs[i] = rr
			return
		}
	}
}

func (set *opSet) isUsing(dig cdig.CDig) bool {
	_, ok := set.using[dig]
	return ok
}

func (set *opSet) markUsing(dig cdig.CDig) {
	set.using[dig] = struct{}{}
}

func (set *opSet) newOp() {
	op := &diffOp{
		done: make(chan struct{}),
		rrs:  set.rrs,
	}
	set.ops = append(set.ops, op)
	set.op = op
}

func (set *opSet) checkBase() {
	if len(set.op.baseInfo) >= set.maxOpSize {
		set.newOp()
	}
}
func (set *opSet) checkReq() {
	if len(set.op.reqInfo) >= set.maxOpSize {
		set.newOp()
	}
}

func (set *opSet) fullBase() bool {
	// assumes baseInfo is filled in previous ops
	return len(set.ops) >= set.maxOps && len(set.op.baseInfo) >= set.maxOpSize
}
func (set *opSet) fullReq() bool {
	// assumes reqInfo is filled in previous ops
	return len(set.ops) >= set.maxOps && len(set.op.reqInfo) >= set.maxOpSize
}

// call with diffLock held
func (set *opSet) buildDiff(
	tx *bbolt.Tx,
	targetDigest cdig.CDig,
	sphps []SphPrefix,
	useRR bool,
) error {
	// find an image with a base with similar data. go backwards on the assumption that recent
	// images with this chunk will be more similar.
	for i := len(sphps) - 1; i >= 0; i-- {
		if res, err := set.s.catalogFindBase(tx, sphps[i]); err == nil {
			set.buildExtendDiff(tx, targetDigest, res, useRR)

			// can't extend recompress or full
			if set.sourcesLeft == 0 || len(set.op.recompress) > 0 || set.fullBase() && set.fullReq() {
				break
			}
		}
	}
	if set.op.hasReq() {
		return nil
	}

	// can't find any base, diff latest against nothing
	sph := sphps[len(sphps)-1]
	foundSph, name := set.s.catalogFindName(tx, sph)
	if len(name) == 0 {
		return errors.New("store path hash not found")
	}
	set.buildExtendDiff(tx, targetDigest, catalogResult{
		reqName: name,
		reqHash: foundSph,
	}, useRR)
	return nil
}

// call with diffLock held
func (set *opSet) buildExtendDiff(
	tx *bbolt.Tx,
	targetDigest cdig.CDig,
	res catalogResult,
	useRR bool,
) {
	firstOp := !set.op.hasReq()

	isManifest := strings.HasPrefix(res.reqName, isManifestPrefix)
	if isManifest && !firstOp {
		// extending is very unlikely to be useful for manifests
		return
	}
	if res.usingBase() {
		if isManifest != strings.HasPrefix(res.baseName, isManifestPrefix) {
			panic("catalog should not match manifest with data")
		}
	}

	var baseIter digestIterator
	if res.usingBase() {
		baseEntries, err := set.s.getDigestsFromImage(nil, tx, res.baseHash, isManifest)
		if err != nil {
			log.Println("failed to get digests for", res.baseHash, res.baseName)
			return
		}
		baseIter = newDigestIterator(baseEntries)
	}
	reqEntries, err := set.s.getDigestsFromImage(nil, tx, res.reqHash, isManifest)
	if err != nil {
		log.Println("failed to get digests for", res.reqHash, res.reqName)
		return
	}
	reqIter := newDigestIterator(reqEntries)

	// find entry
	reqIdx := 0
	for reqIter.digest() != targetDigest {
		if reqIter.next(1) == nil {
			panic("req digest not found in manifest") // shouldn't happen
		}
		reqIdx++
	}

	reqEnt := reqIter.ent()
	var readLog string
	if firstOp {
		readLog = fmt.Sprintf("read /nix/store/%s-%s%s", res.reqHash, res.reqName, reqEnt.Path)
	} else { // later: don't bother logging this
		readLog = fmt.Sprintf("  or /nix/store/%s-%s%s", res.reqHash, res.reqName, reqEnt.Path)
	}

	if firstOp && res.usingBase() {
		if args := getRecompressArgs(reqEnt); len(args) > 0 {
			if err := set.buildRecompress(tx, res, args, baseIter, reqIter, reqEnt); err == nil {
				set.log(res, args[0], true)
				return
			} else {
				log.Println("skipping recompress:", err)
				set.op.resetDiff()
			}
		}
	}

	if useRR {
		rr := set.s.findRecentRead(res.reqHash, reqEnt.Path)
		set.addRecentRead(rr)
		set.shiftMax(rr.reads)
	}

	// try to find some file in base
	if found := baseIter.findFile(reqEnt.Path); found {
		// move to offset within file
		baseIter.d = reqIter.d
		baseIter.next(0) // correct iter in case base file is smaller
	} else {
		// can't find corresponding file, position based on index alone
		baseIter.reset()
		baseIter.next(reqIdx)
	}

	changed := false
	for {
		baseDigest := baseIter.digest()
		if baseDigest != cdig.Zero && !set.fullBase() && !set.isUsing(baseDigest) {
			baseLoc, basePresent := set.s.digestPresent(tx, baseDigest)
			if basePresent {
				set.markUsing(baseDigest)
				set.checkBase()
				set.op.addBase(baseDigest, baseIter.size(), baseLoc)
				changed = true
			}
		}

		reqDigest := reqIter.digest()
		if reqDigest != cdig.Zero && !set.fullReq() && !set.isUsing(reqDigest) {
			reqLoc, reqPresent := set.s.digestPresent(tx, reqDigest)
			if !reqPresent && reqLoc.Addr > 0 && set.s.diffMap[reqLoc] == nil {
				set.markUsing(reqDigest)
				set.checkReq()
				set.op.addReq(reqDigest, reqIter.size(), reqLoc)
				set.s.diffMap[reqLoc] = set.op
				changed = true
			}
		}

		if (baseDigest == cdig.Zero || set.fullBase()) && (reqDigest == cdig.Zero || set.fullReq()) {
			break
		}

		_, newReqEnt := baseIter.next(1), reqIter.next(1)

		if len(set.ops) > 1 && newReqEnt != nil && newReqEnt.Path != reqEnt.Path {
			// we're doing more than one op because we got multiple reads for the same file in
			// succession. we can stop after the file.
			break
		}
	}
	if !changed {
		return
	}
	set.sourcesLeft--

	log.Print(readLog)
	set.log(res, "", firstOp)
}

func (set *opSet) buildRecompress(
	tx *bbolt.Tx,
	res catalogResult,
	args []string,
	baseIter, reqIter digestIterator,
	reqEnt *pb.Entry,
) error {
	// findFile will only return true if it found an entry and it has digests,
	// i.e. missing file, is symlink, inline, etc. will all return false.
	if found := baseIter.findFile(reqEnt.Path); !found {
		return errors.New("base missing corresponding file: " + reqEnt.Path)
	}

	baseEnt := baseIter.ent()
	for baseIter.toFileStart(); baseIter.ent() == baseEnt; baseIter.next(1) {
		baseDigest := baseIter.digest()
		baseLoc, basePresent := set.s.digestPresent(tx, baseDigest)
		if baseLoc.Addr == 0 {
			return errors.New("digest in entry of base digest is not mapped")
		} else if !basePresent {
			// Base is not present, don't bother with recompress (data is already compressed).
			return errors.New("base chunk not present")
		}
		set.op.addBase(baseDigest, baseIter.size(), baseLoc)
	}

	// diff with expanding and recompression
	for reqIter.toFileStart(); reqIter.ent() == reqEnt; reqIter.next(1) {
		reqDigest := reqIter.digest()
		reqLoc := set.s.digestLoc(tx, reqDigest)
		if reqLoc.Addr == 0 {
			return errors.New("digest in entry of req digest is not mapped")
		}
		set.op.addReq(reqDigest, reqIter.size(), reqLoc)
	}

	// No errors, we can enter into diff map. For recompress diff we need to ask for the
	// whole file so we may include chunks we already have, or are already being diffed
	// (though that's very unlikely). In that case just leave the existing entry.
	for _, i := range set.op.reqInfo {
		if set.s.diffMap[i.loc] == nil {
			set.s.diffMap[i.loc] = set.op
		}
	}
	set.op.recompress = args
	return nil
}

func (set *opSet) log(
	res catalogResult,
	recompress string,
	firstOp bool,
) {
	var sb strings.Builder

	if !firstOp {
		fmt.Fprintf(&sb, "+++ ")
	}
	if res.usingBase() {
		fmt.Fprintf(&sb, "diff %s…-%s <~ %s…-%s",
			res.reqHash.String()[:5],
			res.reqName,
			res.baseHash.String()[:5],
			res.baseName,
		)
	} else {
		fmt.Fprintf(&sb, "batch %s…-%s",
			res.reqHash.String()[:5],
			res.reqName,
		)
	}

	for i, op := range set.ops {
		if i != 0 && i != len(set.ops)-1 {
			fmt.Fprintf(&sb, " …%d more…", len(set.ops)-2)
		} else if op.hasBase() {
			fmt.Fprintf(&sb, " [%d:%d <~ %d:%d]", len(op.reqInfo), op.reqTotalSize, len(op.baseInfo), op.baseTotalSize)
		} else {
			fmt.Fprintf(&sb, " [%d:%d]", len(op.reqInfo), op.reqTotalSize)
		}
	}

	if len(recompress) > 0 {
		fmt.Fprintf(&sb, " <using %s>", recompress)
	}

	log.Print(sb.String())
}

// digest iterator

// digestIterator is positioned at first chunk
func newDigestIterator(entries []*pb.Entry) digestIterator {
	i := digestIterator{ents: entries}
	i.reset()
	return i
}

func (i *digestIterator) reset() *pb.Entry {
	i.e, i.d = 0, -cdig.Bytes
	return i.next(1) // move to first actual digest
}

// entry that the current chunk belongs to
func (i *digestIterator) ent() *pb.Entry {
	if i.e >= len(i.ents) {
		return nil
	}
	return i.ents[i.e]
}

// digest of the current chunk
func (i *digestIterator) digest() cdig.CDig {
	ent := i.ent()
	if ent == nil {
		return cdig.Zero
	}
	if i.d+cdig.Bytes > len(ent.Digests) {
		// shouldn't happen, we shouldn't have stopped here
		return cdig.Zero
	}
	return cdig.FromBytes(ent.Digests[i.d : i.d+cdig.Bytes])
}

// size of this chunk
func (i *digestIterator) size() int32 {
	ent := i.ent()
	if ent == nil {
		return -1
	}
	return int32(common.ChunkShift.FileChunkSize(ent.Size, i.d+cdig.Bytes >= len(ent.Digests)))
}

// moves forward n chunks. returns true if valid.
func (i *digestIterator) next(n int) *pb.Entry {
	i.d += n * cdig.Bytes
	for {
		ent := i.ent()
		if ent == nil {
			return nil
		} else if i.d+cdig.Bytes <= len(ent.Digests) {
			return ent
		}
		i.e++
		i.d -= len(ent.Digests)
	}
}

// moves back to start of the current entry. returns true if valid.
func (i *digestIterator) toFileStart() bool {
	ent := i.ent()
	if ent == nil {
		return false
	}
	i.d = 0
	return len(ent.Digests) > 0
}

// finds file matching path. only moves forward. returns true if valid.
func (i *digestIterator) findFile(path string) bool {
	for {
		ent := i.ent()
		if ent == nil {
			return false
		}
		if ent.Path == path {
			return i.toFileStart()
		}
		i.e++
	}
}
