package manifester

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/aws/aws-lambda-go/lambdaurl"
	"github.com/klauspost/compress/zstd"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"golang.org/x/exp/slices"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/pb"
)

const (
	defaultSmallFileCutoff = 224
	maxSmallFileCutoff     = 480

	smallManifestCutoff = 32 * 1024
)

type (
	server struct {
		cfg *Config
		mb  *ManifestBuilder
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

	if r.SmallFileCutoff > maxSmallFileCutoff {
		return fmt.Errorf("small file cutoff too big")
	} else if r.SmallFileCutoff == 0 {
		r.SmallFileCutoff = defaultSmallFileCutoff
	}

	return nil
}

func (s *server) handleManifest(w http.ResponseWriter, req *http.Request) {
	var r ManifestReq
	if err := json.NewDecoder(req.Body).Decode(&r); err != nil {
		w.WriteHeader(http.StatusBadRequest)
	}

	u, err := url.Parse(r.Upstream)
	if err != nil {
		log.Println("bad upstream url:", r)
		w.WriteHeader(http.StatusBadRequest)
		return
	} else if err := s.validateManifestReq(&r, u.Host); err != nil {
		log.Println("validation error:", r)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	log.Println("req", r.StorePathHash, "from", r.Upstream)

	// get narinfo

	u.Path += "/" + r.StorePathHash + ".narinfo"
	narinfoUrl := u.String()
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
	u.Path = "/" + ni.URL
	narUrl := u.String()
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

	log.Println("req", r.StorePathHash, "downloading nar")

	narOut := res.Body

	var decompress *exec.Cmd
	switch ni.Compression {
	case "", "none":
		decompress = nil
	case "xz":
		decompress = exec.Command(common.XzBin, "-d")
	case "zst":
		decompress = exec.Command(common.ZstdBin, "-d")
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

	args := BuildArgs{
		SmallFileCutoff: r.SmallFileCutoff,
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
	entry, err := s.mb.ManifestAsEntry(req.Context(), manifestArgs, common.ManifestContext, manifest)
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

	go s.writeSignedManifest(&r, sb)

	// compress output
	w.Header().Set("Content-Encoding", "zstd")
	zw, err := zstd.NewWriter(w)
	if err != nil {
		log.Println("zstd create:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = zw.Write(sb)
	if err != nil {
		log.Println("zstd write:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = zw.Close()
	if err != nil {
		log.Println("zstd close:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func (s *server) writeSignedManifest(r *ManifestReq, data []byte) {
	// this is in the background
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := s.mb.cs.PutIfNotExists(ctx, ManifestCachePath, r.CacheKey(), data)
	if err != nil {
		log.Println("error writing signed manifest cache:", err)
	}
}

func (s *server) handleChunkDiff(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}

func (s *server) handleChunk(w http.ResponseWriter, r *http.Request) {
	// This is for local testing only, real usage goes to s3 directly

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
	log.Println("chunk", key)
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

	srv := &http.Server{
		Addr:    s.cfg.Bind,
		Handler: mux,
	}
	return srv.ListenAndServe()
}
