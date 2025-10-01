package daemon

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/DataDog/zstd"
	"go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/errgroup"
	"github.com/dnr/styx/common/shift"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

const (
	recentReadExpiry      = 30 * time.Second
	remanifestCacheExpiry = time.Minute

	// only public so they can be referenced by tests
	InitOpSize = 8
	MaxOpSize  = 128      // must be ≤ manifester.ChunkDiffMaxDigests
	MaxOpBytes = 12 << 20 // must be ≤ manifester.ChunkDiffMaxBytes
	MaxDiffOps = 8
	MaxSources = 3
	// start doubling on any file RRs, but require two extra image RRs
	ImageRROffset = 2
)

type (
	digestIterator struct {
		ents []*pb.Entry
		e    int32 // current index in ents
		d    int32 // current digest offset (bytes)
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

	subOp struct {
		baseDigests       []cdig.CDig
		baseInfo          []info
		reqDigests        []cdig.CDig
		reqInfo           []info
		recompress        []string
		baseSize, reqSize int32
	}

	diffOp struct {
		err  error         // result. only written by start, read by wait
		done chan struct{} // closed by start after writing err

		// when building: add non-recompress to [0], add recompress as new pairs.
		// when running: support any combination.
		sops []subOp

		baseTotalChunks, baseTotalSize int32
		reqTotalChunks, reqTotalSize   int32

		usingSph map[Sph]struct{}

		// shared with all ops in opSet.
		// the contents of the recentReads are under diffLock.
		rrs *[MaxSources * 2]*recentRead
	}

	// context for building set of diff ops
	opSet struct {
		s     *Server
		tx    *bbolt.Tx
		op    *diffOp // last op in ops
		ops   []*diffOp
		using map[cdig.CDig]struct{}
		// rrs is indirect so that diffOps can point to it without keeping opSet live
		// first half are for image, second half are for file
		rrs         *[MaxSources * 2]*recentRead
		limitShift  int
		maxOpSize   int // # chunks
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

	remanifestCacheEntry struct {
		when time.Time
		err  error         // only read after done is closed
		done chan struct{} // closed after writing err
	}

	triedRemanifest struct{}
)

func (s *Server) requestChunk(ctx context.Context, loc erofs.SlabLoc, digest cdig.CDig, sphps []SphPrefix) error {
	if s.readKnownMap.Has(loc) {
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
		// force single op
	} else {
		set := newOpSet(s)
		err := s.db.View(func(tx *bbolt.Tx) error {
			return set.buildDiff(tx, digest, sphps, true)
		})
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

	if common.IsNotFound(err) && ctx.Value(triedRemanifest{}) == nil {
		// some required chunk was not found, maybe we can recover by remanifesting
		log.Println("chunk not found, remanifesting")
		if s.doRemanifestReqs(ctx, s.appendRemanifestReqs(nil, op)) != nil {
			return err // don't log; remanifestOp logs failures individually
		}
		// If the set had multiple ops, this one probably finished first and the others are
		// probably still pending, so if we rerequest this now we won't include those. The
		// others are likely to fail for the same reason as this one, so it would be nice to
		// include them. It's too hard to wait on the whole set, so we'll just let the chunks
		// get rerequested directly.
		ctx = context.WithValue(ctx, triedRemanifest{}, true)
		return s.requestChunk(ctx, loc, digest, sphps)
	}

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

	var remanifestReqs []MountReq
	for _, op := range ops {
		if opErr := op.wait(); opErr != nil {
			err = cmp.Or(err, opErr)
			log.Printf("prefetch request failed (%v)", opErr)
			if common.IsNotFound(opErr) && ctx.Value(triedRemanifest{}) == nil {
				// some required chunk was not found, maybe we can recover by remanifesting
				remanifestReqs = s.appendRemanifestReqs(remanifestReqs, op)
			}
		}
	}

	if len(remanifestReqs) > 0 {
		log.Println("chunk not found, remanifesting")
		if s.doRemanifestReqs(ctx, remanifestReqs) == nil {
			ctx = context.WithValue(ctx, triedRemanifest{}, true)
			return s.requestPrefetch(ctx, reqs)
		}
	}

	return err
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
		sphps := sphpsFromLoc(loc)
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
// all chunks must be the same size
func (s *Server) readChunks(
	ctx context.Context, // can be nil if allowMissing is false
	useTx *bbolt.Tx, // optional
	totalSize int64,
	chunkShift shift.Shift,
	locs []erofs.SlabLoc,
	digests []cdig.CDig, // used if allowMissing is true
	sphps []SphPrefix, // used if allowMissing is true
	allowMissing bool,
) ([]byte, error) {
	var firstMissing int
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
			// if this happens we probably have a race between fetching and using manifests
			loc := locs[firstMissing]
			return nil, fmt.Errorf("missing chunk %d:%d", loc.SlabId, loc.Addr)
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
		toRead := min(int(chunkShift.Size()), len(rest))
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
	// TODO: can we figure out at least chunk shift and use a pool?
	// we probably have to just store the size
	// buf := s.chunkPool.Get(int(common.ChunkShift.Size()))
	// defer s.chunkPool.Put(buf)

	chunk, err := s.p().csread.Get(ctx, digest.String(), nil)
	if err != nil {
		return fmt.Errorf("chunk read error: %w", err)
		// } else if len(chunk) > len(buf) || &buf[0] != &chunk[0] {
		// 	return fmt.Errorf("chunk overflowed chunk size: %d > %d", len(chunk), len(buf))
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
			if !op.anyHasBase() {
				s.stats.batchErrs.Add(1)
			} else {
				s.stats.diffErrs.Add(1)
			}
		}

		// clear references to this op from the map
		s.diffLock.Lock()
		for _, sop := range op.sops {
			for _, i := range sop.reqInfo {
				if s.diffMap[i.loc] == reqOp(op) {
					delete(s.diffMap, i.loc)
				}
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

	if !op.anyHasBase() {
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
	diff, lens, err := s.getChunkDiff(ctx, op.sops)
	if err != nil {
		return fmt.Errorf("getChunkDiff: %w", err)
	}
	defer diff.Close()

	baseDatas := make([][]byte, 0, len(op.sops))

	for _, sop := range op.sops {
		if !sop.hasBase() {
			continue
		}

		data := make([]byte, sop.baseSize)
		p := data
		for _, i := range sop.baseInfo {
			var part []byte
			part, p = takePart(p, i.size)
			if err := s.getKnownChunk(i.loc, part); err != nil {
				return fmt.Errorf("getKnownChunk error: %w", err)
			}
		}

		// decompress if needed
		data, err = doDiffDecompress(ctx, data, sop.recompress)
		if err != nil {
			return fmt.Errorf("decompress error: %w", err)
		}

		baseDatas = append(baseDatas, data)
	}

	baseData := common.ContiguousBytes(baseDatas)

	// decompress from diff
	diffCounter := countReader{r: diff}
	reqData, err := io.ReadAll(zstd.NewReaderPatcher(&diffCounter, baseData))
	if err != nil {
		return fmt.Errorf("expandChunkDiff error: %w", err)
	}
	if !op.anyHasBase() {
		s.stats.batchBytes.Add(diffCounter.c)
	} else {
		s.stats.diffBytes.Add(diffCounter.c)
	}

	// write out to slab
	p := reqData
	for sopIdx, sop := range op.sops {
		var data []byte
		data, p = takePart(p, lens[sopIdx])
		if len(sop.recompress) > 0 {
			data, err = doDiffRecompress(ctx, data, sop.recompress)
			if err != nil {
				return fmt.Errorf("recompress error: %w", err)
			}
		}

		if len(data) < int(sop.reqSize) {
			return fmt.Errorf("decompressed data is too short: %d < %d", len(data), sop.reqSize)
		}

		q := data
		for idx, i := range sop.reqInfo {
			var chunk []byte
			chunk, q = takePart(q, i.size)
			if err := s.gotNewChunk(i.loc, sop.reqDigests[idx], chunk); err != nil {
				if len(sop.recompress) > 0 && strings.Contains(err.Error(), "digest mismatch") {
					// we didn't recompress correctly, fall back to single
					// TODO: be able to try with different parameter variants
					return fmt.Errorf("recompress mismatch for %s", sop.reqDigests[idx])
				}
				return fmt.Errorf("gotNewChunk error (diff): %w", err)
			}
		}
	}

	// rest is json stats
	var st manifester.ChunkDiffStats
	if err = json.Unmarshal(p, &st); err == nil {
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

func (s *Server) getWriteFdForSlab(slabId uint16) (int, error) {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	if state := s.stateBySlab[slabId]; state != nil {
		return int(state.writeFd), nil
	}
	return 0, errors.New("slab not loaded or missing write fd")
}

func (s *Server) getReadFdForSlab(slabId uint16) (int, error) {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	if readFd := s.readfdBySlab[slabId].readFd; readFd > 0 {
		return readFd, nil
	}
	return 0, errors.New("slab not loaded or missing read fd")
}

// gotNewChunk may reslice b up to block size and zero up to the new size!
func (s *Server) gotNewChunk(loc erofs.SlabLoc, digest cdig.CDig, b []byte) error {
	if err := digest.Check(b); err != nil {
		return err
	}

	writeFd, err := s.getWriteFdForSlab(loc.SlabId)
	if err != nil {
		// try reading the loc to force cachefiles to load the slab. we haven't
		// written it yet so this will block waiting for whatever diff op is
		// calling us. do it in a new goroutine to avoid a deadlock.
		if readFd, rerr := s.getReadFdForSlab(loc.SlabId); rerr == nil {
			log.Println("forcing reopen on slab", loc.SlabId)
			go unix.Pread(readFd, make([]byte, 1), int64(loc.Addr)<<s.blockShift)
			for i := 0; i < 10 && err != nil; i++ {
				time.Sleep(50 * time.Duration(i+1) * time.Millisecond)
				writeFd, err = s.getWriteFdForSlab(loc.SlabId)
			}
		}
	}
	if err != nil {
		return err
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
	go s.cleanPresentMap(loc)

	return nil
}

func (s *Server) cleanPresentMap(loc erofs.SlabLoc) {
	err := s.db.Batch(func(tx *bbolt.Tx) error {
		sb := tx.Bucket(slabBucket).Bucket(slabKey(loc.SlabId))
		if sb == nil {
			return errors.New("missing slab bucket")
		}
		return sb.Put(addrKey(presentMask|loc.Addr), []byte{})
	})
	if err != nil {
		log.Println("present map record error:", err)
		return
	}
	// we can't clean up presentMap immediately, we need to wait until all read
	// transactions that were started before db.Batch have closed. one solution is to
	// keep a generation number and set of open generations. that's a lot of
	// bookkeeping, though. for now just wait a while. TODO: make this correct
	time.Sleep(time.Minute)
	s.presentMap.Delete(loc)
}

func (s *Server) getChunkDiff(
	ctx context.Context,
	sops []subOp,
) (io.ReadCloser, []int64, error) {
	r := &pb.ManifesterChunkDiffReq{
		Params: &pb.GlobalParams{
			DigestAlgo: cdig.Algo,
			DigestBits: cdig.Bits,
		},
		Req: make([]*pb.ManifesterChunkDiffReq_Req, len(sops)),
	}
	for i, sop := range sops {
		r.Req[i] = &pb.ManifesterChunkDiffReq_Req{
			Bases: cdig.ToSliceAlias(sop.baseDigests),
			Reqs:  cdig.ToSliceAlias(sop.reqDigests),
		}
		if len(sop.recompress) > 0 {
			r.Req[i].ExpandBeforeDiff = sop.recompress[0]
		}
	}
	reqBytes, err := proto.Marshal(r)
	if err != nil {
		return nil, nil, err
	}
	u := strings.TrimSuffix(s.p().params.ChunkDiffUrl, "/") + manifester.ChunkDiffPath
	res, err := common.RetryHttpRequest(ctx, http.MethodPost, u, common.CTProto, reqBytes)
	if err != nil {
		return nil, nil, err
	}

	// parse lengths header if present
	var lens pb.Lengths
	if lensHdr := res.Header.Get(manifester.LengthsHeader); lensHdr != "" {
		lensEnc, err := base64.RawURLEncoding.DecodeString(lensHdr)
		if err == nil {
			err = proto.Unmarshal(lensEnc, &lens)
		}
		if err == nil && len(lens.Length) != len(sops) {
			err = fmt.Errorf("len %d != %d", len(lens.Length), len(sops))
		}
		if err != nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			return nil, nil, fmt.Errorf("bad lengths header: %w", err)
		}
	}
	if len(lens.Length) == 0 {
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return nil, nil, fmt.Errorf("missing lengths header")
	}

	return res.Body, lens.Length, nil
}

// note: called with read-only tx
func (s *Server) getDigestsFromImage(tx *bbolt.Tx, sph Sph, isManifest bool) ([]*pb.Entry, error) {
	if isManifest {
		// get the image sph back. makeManifestSph is its own inverse.
		sph = makeManifestSph(sph)
	}

	v := tx.Bucket(manifestBucket).Get([]byte(sph.String()))
	if v == nil {
		return nil, fmt.Errorf("manifest %q not found", sph.String())
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
		cshift := entry.ChunkShiftDef()
		data, err = s.readChunks(nil, tx, entry.Size, cshift, locs, nil, nil, false)
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
func (s *Server) getManifestLocal(tx *bbolt.Tx, sphStr string) (*pb.Manifest, []cdig.CDig, error) {
	v := tx.Bucket(manifestBucket).Get([]byte(sphStr))
	if v == nil {
		return nil, nil, fmt.Errorf("manifest %q not found", sphStr)
	}
	var sm pb.SignedMessage
	err := proto.Unmarshal(v, &sm)
	if err != nil {
		return nil, nil, err
	}

	// read chunks if needed
	entry := sm.Msg
	data := entry.InlineData
	mdigs := cdig.FromSliceAlias(entry.Digests)
	if len(data) == 0 {
		locs, err := s.lookupLocs(tx, mdigs)
		if err != nil {
			return nil, nil, err
		}
		cshift := entry.ChunkShiftDef()
		data, err = s.readChunks(nil, tx, entry.Size, cshift, locs, nil, nil, false)
		if err != nil {
			return nil, nil, err
		}
	}

	// unmarshal
	var m pb.Manifest
	if err := proto.Unmarshal(data, &m); err != nil {
		return nil, nil, err
	}
	return &m, mdigs, nil
}

func (s *Server) getKnownChunk(loc erofs.SlabLoc, buf []byte) error {
	readFd, err := s.getReadFdForSlab(loc.SlabId)
	if err != nil {
		return err
	}

	// record that we're reading this out of the slab
	s.readKnownMap.Modify(loc, func(i int, _ bool) (int, bool) { return i + 1, true })
	defer s.readKnownMap.Modify(loc, func(i int, _ bool) (int, bool) { return i - 1, i > 1 })

	_, err = unix.Pread(readFd, buf, int64(loc.Addr)<<s.blockShift)
	return err
}

func (s *Server) locPresent(tx *bbolt.Tx, loc erofs.SlabLoc) bool {
	if s.presentMap.Has(loc) {
		return true
	}
	sb := tx.Bucket(slabBucket).Bucket(slabKey(loc.SlabId))
	if sb == nil {
		log.Println("missing slab bucket", loc.SlabId)
		return false
	}
	return sb.Get(addrKey(loc.Addr|presentMask)) != nil
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
		rr.when = time.Now()
		return rr
	}
	rr := &recentRead{when: time.Now()}
	s.recentReads[key] = rr
	return rr
}

func (s *Server) pruneRecentCaches() {
	t := time.NewTicker(min(recentReadExpiry, remanifestCacheExpiry) / 2)
	defer t.Stop()
	for range t.C {
		s.diffLock.Lock()
		now := time.Now()
		maps.DeleteFunc(s.recentReads, func(key string, rr *recentRead) bool {
			return now.Sub(rr.when) > recentReadExpiry
		})
		s.diffLock.Unlock()
		s.remanifestCache.DeleteFunc(func(key string, rr *remanifestCacheEntry) bool {
			return now.Sub(rr.when) > remanifestCacheExpiry
		})
	}
}

func (s *Server) appendRemanifestReqs(reqs []MountReq, op reqOp) []MountReq {
	getSphsFromOp := func(tx *bbolt.Tx) map[Sph]struct{} {
		switch op := op.(type) {
		case *singleOp:
			// we didn't look up sph before, so do it now
			loc := tx.Bucket(chunkBucket).Get(op.digest[:])
			if loc == nil {
				return nil
			}
			for _, sphp := range sphpsFromLoc(loc) {
				sph, name := s.catalogFindName(tx, sphp)
				if name != "" {
					// any one should work, so take first
					return map[Sph]struct{}{sph: struct{}{}}
				}
			}
		case *diffOp:
			return op.usingSph
		}
		return nil
	}

	_ = s.db.View(func(tx *bbolt.Tx) error {
		ib := tx.Bucket(imageBucket)
		for sph := range getSphsFromOp(tx) {
			sphStr := sph.String()
			if slices.ContainsFunc(reqs, func(r MountReq) bool { return r.StorePath == sphStr }) {
				continue
			}
			var img pb.DbImage
			if buf := ib.Get([]byte(sphStr)); buf != nil {
				if err := proto.Unmarshal(buf, &img); err != nil {
					log.Println("image unmarshal:", err)
					continue
				}
			}
			reqs = append(reqs, MountReq{
				StorePath: sphStr,
				Upstream:  img.Upstream,
				NarSize:   img.NarSize,
			})
		}
		return nil
	})

	return reqs
}

func (s *Server) doRemanifestReqs(ctx context.Context, reqs []MountReq) error {
	if len(reqs) == 0 {
		return errors.New("couldn't find any images to remanifest")
	}

	eg := errgroup.WithContext(ctx)
	eg.SetLimit(5)
	var success atomic.Int64
	for _, req := range reqs {
		eg.Go(func() error {
			rr, ok := s.remanifestCache.GetOrPut(req.StorePath, &remanifestCacheEntry{
				when: time.Now(),
				done: make(chan struct{}),
			})
			if ok {
				<-rr.done
				if rr.err == nil {
					success.Add(1)
				}
				return nil
			}

			_, err := s.getManifestFromManifester(ctx, req.Upstream, req.StorePath, req.NarSize)

			rr.err = err
			close(rr.done)
			s.remanifestCache.WithValue(req.StorePath, func(rr *remanifestCacheEntry) { rr.when = time.Now() })

			if err == nil {
				success.Add(1)
			} else {
				log.Printf("remanifest of %s failed: %v", req.StorePath, err)
			}
			return nil // don't cancel others
		})
	}
	if eg.Wait(); success.Load() == 0 {
		return errors.New("no remanifest succeeded")
	}
	return nil
}

// single op

func (op *singleOp) wait() error {
	<-op.done
	return op.err
}

// subop

func (sop *subOp) addBase(digest cdig.CDig, size int32, loc erofs.SlabLoc) {
	sop.baseDigests = append(sop.baseDigests, digest)
	sop.baseInfo = append(sop.baseInfo, info{size, loc})
	sop.baseSize += size
}

func (sop *subOp) addReq(digest cdig.CDig, size int32, loc erofs.SlabLoc) {
	sop.reqDigests = append(sop.reqDigests, digest)
	sop.reqInfo = append(sop.reqInfo, info{size, loc})
	sop.reqSize += size
}

func (sop *subOp) hasBase() bool {
	return len(sop.baseInfo) > 0
}

func (sop *subOp) hasReq() bool {
	return len(sop.reqInfo) > 0
}

func (sop *subOp) appendString(w io.Writer) {
	fmt.Fprintf(w, "[%d:%d", len(sop.reqInfo), sop.reqSize)
	if sop.hasBase() {
		fmt.Fprintf(w, " <~ %d:%d", len(sop.baseInfo), sop.baseSize)
	}
	if len(sop.recompress) > 0 {
		fmt.Fprintf(w, " | %s", sop.recompress[0])
	}
	fmt.Fprint(w, "]")
}

// diff op

func (op *diffOp) wait() error {
	<-op.done
	return op.err
}

func (op *diffOp) anyHasBase() bool {
	for _, sop := range op.sops {
		if sop.hasBase() {
			return true
		}
	}
	return false
}

func (op *diffOp) anyHasReq() bool {
	for _, sop := range op.sops {
		if sop.hasReq() {
			return true
		}
	}
	return false
}

func (op *diffOp) addBase(sph Sph, digest cdig.CDig, size int32, loc erofs.SlabLoc) {
	op.usingSph[sph] = struct{}{}
	op.sops[0].addBase(digest, size, loc)
	op.baseTotalChunks += 1
	op.baseTotalSize += size
}

func (op *diffOp) addReq(sph Sph, digest cdig.CDig, size int32, loc erofs.SlabLoc) {
	op.usingSph[sph] = struct{}{}
	op.sops[0].addReq(digest, size, loc)
	op.reqTotalChunks += 1
	op.reqTotalSize += size
}

func (op *diffOp) addRecompress(sphs map[Sph]struct{}, sop subOp) {
	maps.Copy(op.usingSph, sphs)
	op.sops = append(op.sops, sop)
	op.baseTotalChunks += int32(len(sop.baseInfo))
	op.baseTotalSize += sop.baseSize
	op.reqTotalChunks += int32(len(sop.reqInfo))
	op.reqTotalSize += sop.reqSize
}

// op set

func newOpSet(s *Server) *opSet {
	set := &opSet{
		s:           s,
		using:       make(map[cdig.CDig]struct{}),
		rrs:         new([MaxSources * 2]*recentRead),
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

func (set *opSet) updateRecentReads(sph Sph, path string) {
	addRecentRead := func(rr *recentRead, rrs []*recentRead) {
		for i, have := range rrs {
			if have == rr {
				return
			} else if have == nil {
				rrs[i] = rr
				return
			}
		}
	}
	imageRR := set.s.findRecentRead(sph, "")
	addRecentRead(imageRR, set.rrs[:MaxSources])
	fileRR := set.s.findRecentRead(sph, path)
	addRecentRead(fileRR, set.rrs[MaxSources:])
	set.shiftMax(max(imageRR.reads-ImageRROffset, fileRR.reads))
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
		done:     make(chan struct{}),
		sops:     make([]subOp, 1),
		usingSph: make(map[Sph]struct{}),
		rrs:      set.rrs,
	}
	set.ops = append(set.ops, op)
	set.op = op
}

func (set *opSet) checkReq() {
	if len(set.op.sops[0].reqInfo) >= set.maxOpSize || set.op.reqTotalSize >= MaxOpBytes {
		set.newOp()
	}
}

func (set *opSet) fullBase() bool {
	// assumes baseInfo is filled in previous ops
	return len(set.ops) >= set.maxOps && (int(set.op.baseTotalChunks) >= set.maxOpSize || set.op.baseTotalSize >= MaxOpBytes)
}
func (set *opSet) fullReq() bool {
	// assumes reqInfo is filled in previous ops
	return len(set.ops) >= set.maxOps && (int(set.op.reqTotalChunks) >= set.maxOpSize || set.op.reqTotalSize >= MaxOpBytes)
}
func (set *opSet) subOpFits(sop subOp) bool {
	return int(set.op.baseTotalChunks)+len(sop.baseInfo) <= set.maxOpSize &&
		(set.op.baseTotalSize+sop.baseSize) <= MaxOpBytes &&
		int(set.op.reqTotalChunks)+len(sop.reqInfo) <= set.maxOpSize &&
		(set.op.reqTotalSize+sop.reqSize) <= MaxOpBytes
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

			// can't extend if full
			if set.sourcesLeft == 0 || set.fullBase() && set.fullReq() {
				break
			}
		}
	}
	if set.op.anyHasReq() {
		return nil
	}

	// can't find any base, diff latest against nothing
	sphp := sphps[len(sphps)-1]
	foundSph, name := set.s.catalogFindName(tx, sphp)
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
	firstOp := !set.op.anyHasReq()

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
		baseEntries, err := set.s.getDigestsFromImage(tx, res.baseHash, isManifest)
		if err != nil {
			log.Println("failed to get digests for", res.baseHash, res.baseName, err)
			return
		}
		baseIter = newDigestIterator(baseEntries)
	}
	reqEntries, err := set.s.getDigestsFromImage(tx, res.reqHash, isManifest)
	if err != nil {
		log.Println("failed to get digests for", res.reqHash, res.reqName, err)
		return
	}
	reqIter := newDigestIterator(reqEntries)

	// find entry
	reqIdx := 0
	for reqIter.digest() != targetDigest {
		if reqIter.next(1) == nil {
			// this shouldn't happen
			log.Printf("bug: req digest not found in manifest: %s in %s-%s", targetDigest, res.reqHash, res.reqName)
			return
		}
		reqIdx++
	}

	path := reqIter.ent().Path
	var readLog string
	if isManifest {
		readLog = fmt.Sprintf("read manifest %s-%s", res.reqHash, strings.TrimPrefix(res.reqName, isManifestPrefix))
	} else if firstOp {
		readLog = fmt.Sprintf("read %s-%s%s", res.reqHash, res.reqName, path)
	} else { // later: don't bother logging this
		readLog = fmt.Sprintf("  or %s-%s%s", res.reqHash, res.reqName, path)
	}

	if useRR {
		set.updateRecentReads(res.reqHash, path)
	}

	// try to find same file in base
	if found := baseIter.findFile(path); found {
		// move to offset within file
		baseIter.d = reqIter.d
		baseIter.next(0) // correct iter in case base file is smaller
	} else {
		// can't find corresponding file, position based on index alone
		baseIter.reset()
		baseIter.next(reqIdx)
	}

	changed := false
	newFile := true
	for {
		if newFile && res.usingBase() {
			if args := getRecompressArgs(reqIter.ent()); len(args) > 0 {
				if newBaseIter, newReqIter, err := set.buildRecompress(tx, res, args, baseIter, reqIter); err == nil {
					baseIter, reqIter = newBaseIter, newReqIter
					changed = true

					if (baseIter.ent() == nil || set.fullBase()) && (reqIter.ent() == nil || set.fullReq()) {
						break
					}

					continue
				}
			}
		}

		reqDigest := reqIter.digest()
		if reqDigest != cdig.Zero && !set.fullReq() && !set.isUsing(reqDigest) {
			reqLoc, reqPresent := set.s.digestPresent(tx, reqDigest)
			if !reqPresent && reqLoc.Addr > 0 && set.s.diffMap[reqLoc] == nil {
				set.markUsing(reqDigest)
				set.checkReq()
				set.op.addReq(res.reqHash, reqDigest, reqIter.size(), reqLoc)
				set.s.diffMap[reqLoc] = set.op
				changed = true
			}
		}

		// fill base only if room in this op, don't make more ops just for base
		baseDigest := baseIter.digest()
		if baseDigest != cdig.Zero && int(set.op.baseTotalChunks) < set.maxOpSize && set.op.baseTotalSize < MaxOpBytes && !set.isUsing(baseDigest) {
			baseLoc, basePresent := set.s.digestPresent(tx, baseDigest)
			if basePresent {
				set.markUsing(baseDigest)
				set.op.addBase(res.baseHash, baseDigest, baseIter.size(), baseLoc)
				changed = true
			}
		}

		if (baseDigest == cdig.Zero || set.fullBase()) && (reqDigest == cdig.Zero || set.fullReq()) {
			break
		}

		_, newReqEnt := baseIter.next(1), reqIter.next(1)
		if newFile = newReqEnt != nil && newReqEnt.Path != path; newFile {
			path = newReqEnt.Path
		}

		if len(set.ops) > 1 && newFile {
			// we're doing more than one op because we got multiple reads for the same file in
			// succession. we can stop after the file.
			// TODO: we added image-RRs, so we should update this condition: if we hit an image
			// RR (in addition to or instead of a file RR) then don't break here.
			break
		}
	}
	if !changed {
		return
	}
	set.sourcesLeft--

	log.Print(readLog)
	set.log(res, firstOp)
}

func (set *opSet) buildRecompress(
	tx *bbolt.Tx,
	res catalogResult,
	args []string,
	baseIter, reqIter digestIterator,
) (newBaseIter, newReqIter digestIterator, retErr error) {
	reqEnt := reqIter.ent()
	// findFile will only return true if it found an entry and it has digests,
	// i.e. missing file, is symlink, inline, etc. will all return false.
	if found := baseIter.findFile(reqEnt.Path); !found {
		// kind of gross, but we need to handle this for linux:
		// try to find corresponding module file ignoring version number in path
		if strings.HasPrefix(res.baseName, "linux") && isLinuxKoXz(reqEnt) {
			// path is like: /lib/modules/6.11.7/kernel/net/dccp/dccp.ko.xz
			parts := strings.Split(reqEnt.Path, "/")
			pre := strings.Join(parts[:3], "/")
			post := strings.Join(parts[4:], "/")
			re, err := regexp.Compile(`^` + regexp.QuoteMeta(pre) + `/[^/]+/` + regexp.QuoteMeta(post) + `$`)
			if err == nil {
				baseIter.reset()
				found = baseIter.findFileFunc(re.MatchString)
			}
		}
		if !found {
			retErr = errors.New("base missing corresponding file: " + reqEnt.Path)
			return
		}
	}

	baseEnt := baseIter.ent()

	sphs := make(map[Sph]struct{})
	sop := subOp{
		baseDigests: make([]cdig.CDig, 0, baseEnt.Chunks()),
		baseInfo:    make([]info, 0, baseEnt.Chunks()),
		reqDigests:  make([]cdig.CDig, 0, reqEnt.Chunks()),
		reqInfo:     make([]info, 0, reqEnt.Chunks()),
		recompress:  args,
	}

	for baseIter.toFileStart(); baseIter.ent() == baseEnt; baseIter.next(1) {
		baseDigest := baseIter.digest()
		baseLoc, basePresent := set.s.digestPresent(tx, baseDigest)
		if baseLoc.Addr == 0 {
			retErr = errors.New("digest in entry of base digest is not mapped")
			return
		} else if !basePresent {
			// Base is not present, don't bother with recompress (data is already compressed).
			retErr = errors.New("base chunk not present")
			return
		}
		sphs[res.baseHash] = struct{}{}
		sop.addBase(baseDigest, baseIter.size(), baseLoc)
	}

	for reqIter.toFileStart(); reqIter.ent() == reqEnt; reqIter.next(1) {
		reqDigest := reqIter.digest()
		reqLoc := set.s.digestLoc(tx, reqDigest)
		if reqLoc.Addr == 0 {
			retErr = errors.New("digest in entry of req digest is not mapped")
			return
		}
		sphs[res.reqHash] = struct{}{}
		sop.addReq(reqDigest, reqIter.size(), reqLoc)
	}

	if !set.subOpFits(sop) {
		retErr = errors.New("recompress would make op too big")
		return
	}

	set.op.addRecompress(sphs, sop)

	// For recompress diff we need to ask for the whole file so we may include chunks we
	// already have, or are already being diffed (though that's very unlikely). In that case
	// just leave the existing entry.
	for _, i := range sop.reqInfo {
		if set.s.diffMap[i.loc] == nil {
			set.s.diffMap[i.loc] = set.op
		}
	}
	return baseIter, reqIter, nil
}

func (set *opSet) log(
	res catalogResult,
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
			if i == 1 {
				fmt.Fprintf(&sb, " …%d…", len(set.ops)-2)
			}
		} else {
			fmt.Fprint(&sb, " ")
			for j, sop := range op.sops {
				if j != 0 && j != len(op.sops)-1 {
					if j == 1 {
						fmt.Fprintf(&sb, "…%d…", len(op.sops)-2)
					}
				} else {
					sop.appendString(&sb)
				}
			}
		}
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
	if i.e >= int32(len(i.ents)) {
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
	if i.d+cdig.Bytes > int32(len(ent.Digests)) {
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
	cshift := ent.ChunkShiftDef()
	return int32(cshift.FileChunkSize(ent.Size, i.d+cdig.Bytes >= int32(len(ent.Digests))))
}

// moves forward n chunks. returns true if valid.
func (i *digestIterator) next(n int) *pb.Entry {
	i.d += int32(n * cdig.Bytes)
	for {
		ent := i.ent()
		if ent == nil {
			return nil
		} else if i.d+cdig.Bytes <= int32(len(ent.Digests)) {
			return ent
		}
		i.e++
		i.d -= int32(len(ent.Digests))
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

func (i *digestIterator) findFileFunc(f func(string) bool) bool {
	for {
		ent := i.ent()
		if ent == nil {
			return false
		}
		if f(ent.Path) {
			return i.toFileStart()
		}
		i.e++
	}
}

func takePart[I int | int32 | int64](b []byte, s I) (part, rest []byte) {
	// slice with cap to force copy if less than block size
	return b[:s:s], b[s:]
}
