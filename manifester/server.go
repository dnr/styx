package manifester

import (
	"bytes"
	"cmp"
	"compress/gzip"
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
	"sync"
	"time"

	"github.com/DataDog/zstd"
	"github.com/aws/aws-lambda-go/lambdaurl"
	"golang.org/x/exp/slices"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/errgroup"
)

const (
	defaultSmallFileCutoff = 224
	// maxSmallFileCutoff = 480

	smallManifestCutoff = 32 * 1024
)

var (
	errClosed = errors.New("closed")
)

type (
	server struct {
		cfg *Config
		mb  *ManifestBuilder

		digestBytes int

		httpServer *http.Server
	}

	Config struct {
		Bind             string
		AllowedUpstreams []string

		ChunkDiffZstdLevel int
		ChunkDiffParallel  int
	}
)

func NewManifestServer(cfg Config, mb *ManifestBuilder) (*server, error) {
	return &server{
		cfg: &cfg,
		mb:  mb,

		digestBytes: int(mb.params.DigestBits) >> 3,
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

	cmpSb, err := s.mb.Build(req.Context(), r.Upstream, r.StorePathHash, r.ShardTotal, r.ShardIndex, "")

	if err != nil {
		w.Header().Set("Content-Type", "text/plain")
		switch {
		case errors.Is(err, ErrReq):
			w.WriteHeader(http.StatusExpectationFailed)
		case errors.Is(err, ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
		case errors.Is(err, ErrInternal):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
		w.Write([]byte(err.Error()))
		return
	}

	if r.ShardIndex != 0 {
		w.Write([]byte(fmt.Sprintf("shard %d/%d ok", r.ShardIndex, r.ShardTotal)))
		return
	}

	w.Header().Set("Content-Encoding", "zstd")
	w.Write(cmpSb)
}

func (s *server) handleChunkDiff(w http.ResponseWriter, req *http.Request) {
	var r ChunkDiffReq
	if err := json.NewDecoder(req.Body).Decode(&r); err != nil {
		log.Println("json parse error:", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// load requested chunks
	start := time.Now()

	var baseData, reqData []byte
	var baseErr, reqErr error
	var wg sync.WaitGroup

	// fetch both in parallel
	egCtx := errgroup.WithContext(req.Context())
	egCtx.SetLimit(s.cfg.ChunkDiffParallel)
	wg.Add(2)
	go func() {
		baseData, baseErr = s.expand(egCtx, r.Bases, r.ExpandBeforeDiff)
		wg.Done()
	}()
	go func() {
		reqData, reqErr = s.expand(egCtx, r.Reqs, r.ExpandBeforeDiff)
		wg.Done()
	}()
	// wait for both
	wg.Wait()
	if baseErr != nil {
		log.Println("chunk read (base) error", baseErr)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if reqErr != nil {
		log.Println("chunk read (req) error", reqErr)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	dlDone := time.Now()

	w.Header().Set("Content-Type", "application/octet-stream")

	// max size of json-encoded stats. if we add more stats, may need to increase this
	const statsSpace = 256

	cw := countWriter{w: w}
	zw := zstd.NewWriterPatcher(&cw, s.cfg.ChunkDiffZstdLevel, baseData, int64(len(reqData))+statsSpace)
	if _, err := zw.Write(reqData); err != nil {
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

	stats := ChunkDiffStats{
		BaseChunks: len(r.Bases) / s.digestBytes,
		BaseBytes:  len(baseData),
		ReqChunks:  len(r.Reqs) / s.digestBytes,
		ReqBytes:   len(reqData),
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

func (s *server) expand(egCtx *errgroup.Group, digests []byte, expand string) ([]byte, error) {
	if len(digests) == 0 {
		return nil, nil
	}

	switch expand {
	case ExpandGz:
		pr, pw := io.Pipe()
		go func() {
			pw.CloseWithError(s.fetchChunkSeries(egCtx, digests, pw))
		}()
		gzr, err := gzip.NewReader(pr)
		if err != nil {
			pr.CloseWithError(err) // cause writes to write end to fail
			return nil, err
		}
		return io.ReadAll(gzr)

	case ExpandXz:
		decompress := exec.CommandContext(egCtx, common.XzBin, "-d")
		pw, err := decompress.StdinPipe()
		if err != nil {
			return nil, err
		}
		pr, err := decompress.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err = decompress.Start(); err != nil {
			return nil, err
		}
		go func() {
			s.fetchChunkSeries(egCtx, digests, pw)
			pw.Close()
		}()
		out, readErr := io.ReadAll(pr)
		return common.ValOrErr(out, cmp.Or(decompress.Wait(), readErr))

	default:
		var out bytes.Buffer
		out.Grow((len(digests) / s.digestBytes) << (s.mb.params.ChunkShift - 1))
		err := s.fetchChunkSeries(egCtx, digests, &out)
		return common.ValOrErr(out.Bytes(), err)
	}
}

func (s *server) fetchChunkSeries(egCtx *errgroup.Group, digests []byte, out io.Writer) error {
	// TODO: ew, use separate setting?
	cs := s.mb.cs

	chs := make(chan chan []byte, egCtx.Limit())
	go func() {
		for len(digests) > 0 && egCtx.Err() == nil {
			digestStr := common.DigestStr(digests[:s.digestBytes])
			digests = digests[s.digestBytes:]
			ch := make(chan []byte)
			chs <- ch
			egCtx.Go(func() error {
				b, err := cs.Get(egCtx, ChunkReadPath, digestStr, nil)
				ch <- b
				return err
			})
		}
		close(chs)
	}()

	for ch := range chs {
		if b := <-ch; len(b) > 0 && egCtx.Err() == nil {
			if _, err := out.Write(b); err != nil {
				egCtx.Cancel(err)
			}
		}
	}
	return context.Cause(egCtx)
}

func (s *server) handleChunk(w http.ResponseWriter, r *http.Request) {
	// This is for local testing only, real usage goes to s3 directly!
	localWrite, ok := s.mb.cs.(*localChunkStoreWrite)
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
