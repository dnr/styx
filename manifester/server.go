package manifester

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/DataDog/zstd"
	"github.com/aws/aws-lambda-go/lambdaurl"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"golang.org/x/exp/slices"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/pb"
)

const (
	defaultSmallFileCutoff = 224
	// maxSmallFileCutoff     = 480
	// TODO: fix and turn on by default
	defaultExpandManFiles = false

	smallManifestCutoff = 32 * 1024
)

var (
	errClosed = errors.New("closed")
)

type (
	server struct {
		cfg *Config
		mb  *ManifestBuilder
		zp  *common.ZstdCtxPool

		httpServer *http.Server
	}

	Config struct {
		Bind             string
		AllowedUpstreams []string

		ManifestBuilder *ManifestBuilder

		// Verify loaded narinfo against these keys. Nil means don't verify.
		PublicKeys []signature.PublicKey
		// Sign manifests with these keys.
		SigningKeys []signature.SecretKey
	}
)

func ManifestServer(cfg Config) (*server, error) {
	return &server{
		cfg: &cfg,
		mb:  cfg.ManifestBuilder,
		zp:  common.NewZstdCtxPool(),
	}, nil
}

func (s *server) validateManifestReq(r *ManifestReq, upstreamHost string) error {
	if r.ChunkShift != int(s.mb.params.ChunkShift) {
		return fmt.Errorf("mismatched chunk shift (this server uses %d, not %d)",
			s.mb.params.ChunkShift, r.ChunkShift)
	} else if r.DigestAlgo != s.mb.params.DigestAlgo {
		return fmt.Errorf("mismatched chunk shift (this server uses %s, not %s)",
			s.mb.params.DigestAlgo, r.DigestAlgo)
	} else if r.DigestBits != int(s.mb.params.DigestBits) {
		return fmt.Errorf("mismatched chunk shift (this server uses %d, not %d)",
			s.mb.params.DigestBits, r.DigestBits)
	}

	if !slices.Contains(s.cfg.AllowedUpstreams, upstreamHost) {
		return fmt.Errorf("invalid upstream %q", upstreamHost)
	}

	return nil
}

func (s *server) handleManifest(w http.ResponseWriter, req *http.Request) {
	var r ManifestReq
	if err := json.NewDecoder(req.Body).Decode(&r); err != nil {
		log.Println("json parse error:", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	upstreamUrl, err := url.Parse(r.Upstream)
	if err != nil {
		log.Println("bad upstream url:", r.Upstream)
		w.WriteHeader(http.StatusBadRequest)
		return
	} else if err := s.validateManifestReq(&r, upstreamUrl.Host); err != nil {
		log.Println("validation error:", err, "for", r)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	log.Println("req", r.StorePathHash, "from", r.Upstream)

	// get narinfo

	narinfoUrl := upstreamUrl.JoinPath(r.StorePathHash + ".narinfo").String()
	res, err := http.Get(narinfoUrl)
	if err != nil {
		log.Println("upstream http error:", err, "for", narinfoUrl)
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		log.Println("upstream http error:", res.Status, "for", narinfoUrl)
		if res.StatusCode == http.StatusNotFound {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}

	var rawNarinfo bytes.Buffer
	ni, err := narinfo.Parse(io.TeeReader(res.Body, &rawNarinfo))
	if err != nil {
		log.Println("narinfo parse error:", err, "for", narinfoUrl)
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}

	// verify signature

	if !signature.VerifyFirst(ni.Fingerprint(), ni.Signatures, s.cfg.PublicKeys) {
		log.Printf("signature validation failed for narinfo %s: %#v", narinfoUrl, ni)
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}

	log.Println("req", r.StorePathHash, "got narinfo", ni.StorePath[44:], ni.FileSize, ni.NarSize)

	// download nar

	// start := time.Now()
	narUrl := upstreamUrl.JoinPath(ni.URL).String()
	res, err = http.Get(narUrl)
	if err != nil {
		log.Println("nar http error:", err, "for", narUrl)
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		log.Println("nar http status: ", res.Status, "for", narUrl)
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}

	// log.Println("req", r.StorePathHash, "downloading nar")

	narOut := res.Body

	var decompress *exec.Cmd
	switch ni.Compression {
	case "", "none":
		decompress = nil
	case "xz":
		decompress = exec.Command(common.XzBin, "-d")
	// case "zst":
	// TODO: use in-memory pipe?
	// 	decompress = exec.Command(common.ZstdBin, "-d")
	default:
		log.Println("unknown compression:", ni.Compression, "for", narUrl)
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}
	if decompress != nil {
		decompress.Stdin = res.Body
		narOut, err = decompress.StdoutPipe()
		if err != nil {
			log.Println("can't create stdout pipe")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		decompress.Stderr = os.Stderr
		if err = decompress.Start(); err != nil {
			log.Print("nar decompress start error: ", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	// set up to hash nar

	narHasher, err := hash.New(ni.NarHash.HashType)
	if err != nil {
		log.Print("invalid NarHash type:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// TODO: make args configurable again (hashed in manifest cache key)
	args := &BuildArgs{
		SmallFileCutoff: defaultSmallFileCutoff,
		ExpandManFiles:  defaultExpandManFiles,
		ShardTotal:      r.ShardTotal,
		ShardIndex:      r.ShardIndex,
	}
	manifest, err := s.mb.Build(req.Context(), args, io.TeeReader(narOut, narHasher))
	if err != nil {
		log.Println("manifest generation error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// verify nar hash

	if narHasher.SRIString() != ni.NarHash.SRIString() {
		log.Println("nar hash mismatch")
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}

	if decompress != nil {
		if err = decompress.Wait(); err != nil {
			log.Println("nar decompress error: ", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// elapsed := time.Since(start)
		// ps := decompress.ProcessState
		// log.Printf("downloaded %s [%d bytes] in %s [decmp %s user, %s sys]: %.3f MB/s",
		// 	ni.URL, size, elapsed, ps.UserTime(), ps.SystemTime(),
		// 	float64(size)/elapsed.Seconds()/1e6)
	}

	// if we're not shard 0, we're done
	if r.ShardIndex != 0 {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
		return
	}

	// add metadata

	nipb := &pb.NarInfo{
		StorePath:   ni.StorePath,
		Url:         ni.URL,
		Compression: ni.Compression,
		FileHash:    ni.FileHash.NixString(),
		FileSize:    int64(ni.FileSize),
		NarHash:     ni.NarHash.NixString(),
		NarSize:     int64(ni.NarSize),
		References:  ni.References,
		Deriver:     ni.Deriver,
		System:      ni.System,
		Signatures:  make([]string, len(ni.Signatures)),
		Ca:          ni.CA,
	}
	for i, sig := range ni.Signatures {
		nipb.Signatures[i] = sig.String()
	}
	manifest.Meta = &pb.ManifestMeta{
		NarinfoUrl:    narinfoUrl,
		Narinfo:       nipb,
		Generator:     "styx-" + common.Version,
		GeneratedTime: time.Now().Unix(),
	}

	// turn into entry (maybe chunk)

	manifestArgs := BuildArgs{SmallFileCutoff: smallManifestCutoff}
	path := common.ManifestContext + "/" + path.Base(ni.StorePath)
	entry, err := s.mb.ManifestAsEntry(req.Context(), &manifestArgs, path, manifest)
	if err != nil {
		log.Println("make manifest entry error:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	sb, err := common.SignMessageAsEntry(s.cfg.SigningKeys, s.mb.params, entry)
	if err != nil {
		log.Println("sign error:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// write to cache (it'd be nice to return and do this in the background, but that doesn't
	// work on lambda)
	// TODO: we shouldn't write to cache unless we know for sure that other shards are done.
	// (or else change client to re-request manifest on missing)
	cmpSb, err := s.mb.cs.PutIfNotExists(req.Context(), ManifestCachePath, r.CacheKey(), sb)
	if err != nil {
		log.Println("error writing signed manifest cache:", err)
	}
	if cmpSb == nil {
		// already exists in cache, need to compress ourselves
		z := s.zp.Get()
		defer s.zp.Put(z)
		cmpSb, err = z.Compress(nil, sb)
		if err != nil {
			log.Println("compress error:", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Encoding", "zstd")
	w.Write(cmpSb)
}

func (s *server) handleChunkDiff(w http.ResponseWriter, req *http.Request) {
	const level = 3             // TODO: configurable
	const chunksInParallel = 60 // TODO: configurable

	var r ChunkDiffReq
	if err := json.NewDecoder(req.Body).Decode(&r); err != nil {
		log.Println("json parse error:", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// load requested chunks
	start := time.Now()

	var baseData, reqData bytes.Buffer
	baseErrCh := make(chan error)
	reqErrCh := make(chan error)

	// start fetching both
	go func() {
		baseErrCh <- s.fetchChunkSeries(req.Context(), r.Bases, chunksInParallel/2, &baseData)
	}()
	go func() {
		reqErrCh <- s.fetchChunkSeries(req.Context(), r.Reqs, chunksInParallel/2, &reqData)
	}()
	// wait for both
	if baseErr := <-baseErrCh; baseErr != nil {
		log.Println("chunk read (base) error", baseErr)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if reqErr := <-reqErrCh; reqErr != nil {
		log.Println("chunk read (req) error", reqErr)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	dlDone := time.Now()

	w.Header().Set("Content-Type", "application/octet-stream")

	// max size of json-encoded stats. if we add more stats, may need to increase this
	const statsSpace = 256

	cw := countWriter{w: w}
	zw := zstd.NewWriterPatcher(&cw, level, baseData.Bytes(), int64(reqData.Len())+statsSpace)
	if _, err := zw.Write(reqData.Bytes()); err != nil {
		log.Printf("zstd write error %v", err)
		w.WriteHeader(http.StatusInternalServerError) // this will fail if zstd wrote anything
		return
	}

	// flush to get accurate count in cw
	if err := zw.Flush(); err != nil {
		log.Printf("zstd flush error %v", err)
		w.WriteHeader(http.StatusInternalServerError) // this will fail if zstd wrote anything
		return
	}

	digestBytes := int(s.mb.params.DigestBits >> 3)
	stats := ChunkDiffStats{
		BaseChunks: len(r.Bases) / digestBytes,
		BaseBytes:  baseData.Len(),
		ReqChunks:  len(r.Reqs) / digestBytes,
		ReqBytes:   reqData.Len(),
		DiffBytes:  cw.c,
		DlTotalMs:  dlDone.Sub(start).Milliseconds(),
		ZstdMs:     time.Now().Sub(dlDone).Milliseconds(),
	}
	statsEnc, err := json.Marshal(stats)
	if err != nil || len(statsEnc) > statsSpace {
		statsEnc = []byte("{}")
	}
	if pad := statsSpace - len(statsEnc); pad > 0 {
		statsEnc = append(statsEnc, bytes.Repeat([]byte{'\n'}, pad)...)
	}
	if _, err := zw.Write(statsEnc); err != nil {
		log.Printf("zstd write stats error %v", err)
		w.WriteHeader(http.StatusInternalServerError) // this will fail if zstd wrote anything
		return
	}
	if err := zw.Close(); err != nil {
		log.Printf("zstd close error %v", err)
		w.WriteHeader(http.StatusInternalServerError) // this will fail if zstd wrote anything
		return
	}

	log.Printf("diff done %#v", stats)
}

func (s *server) fetchChunkSeries(ctx context.Context, digests []byte, parallel int, out io.Writer) error {
	digestBytes := s.mb.params.DigestBits >> 3
	// TODO: ew, use separate setting?
	cs := s.cfg.ManifestBuilder.cs

	type res struct {
		b   []byte
		err error
	}
	chs := make(chan chan res, parallel)
	go func() {
		for len(digests) > 0 {
			digest := digests[:digestBytes]
			digests = digests[digestBytes:]
			ch := make(chan res)
			chs <- ch
			go func() {
				digestStr := common.DigestStr(digest)
				b, err := cs.Get(ctx, ChunkReadPath, digestStr, nil)
				ch <- res{b, err}
			}()
		}
		close(chs)
	}()

	for ch := range chs {
		res := <-ch
		if res.err != nil {
			return res.err
		} else if _, err := out.Write(res.b); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) handleChunk(w http.ResponseWriter, r *http.Request) {
	// This is for local testing only, real usage goes to s3 directly!
	localWrite, ok := s.cfg.ManifestBuilder.cs.(*localChunkStoreWrite)
	if !ok {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	if l := len(parts); l < 2 {
		w.WriteHeader(http.StatusNotFound)
		return
	} else if parts[l-2] != "chunk" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	key := parts[len(parts)-1]

	fn := path.Join(localWrite.dir, key)
	f, err := os.Open(fn)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer f.Close()
	// log.Println("get chunk", key)
	w.Header().Set("Content-Encoding", "zstd")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, f)
}

func (s *server) Run() error {
	mux := http.NewServeMux()
	mux.HandleFunc(ManifestPath, s.handleManifest)
	mux.HandleFunc(ChunkDiffPath, s.handleChunkDiff)
	mux.HandleFunc(ChunkReadPath, s.handleChunk)

	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		lambdaurl.Start(mux)
		return nil
	}

	s.httpServer = &http.Server{
		Addr:    s.cfg.Bind,
		Handler: mux,
	}
	return s.httpServer.ListenAndServe()
}

func (s *server) Stop() {
	_ = s.httpServer.Close()
}
