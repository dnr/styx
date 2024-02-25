package manifester

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"golang.org/x/exp/slices"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/pb"
)

const (
	defaultTailCutoff = 480
	defaultChunkSize  = 1 << 16
	defaultHashAlgo   = "sha256"
	defaultHashBits   = 192
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

func (s *server) validateManifestReq(r *ManifestReq) error {
	if !slices.Contains(s.cfg.AllowedUpstreams, r.Upstream) {
		return fmt.Errorf("invalid upstream %q", r.Upstream)
	}
	return nil
}

func (s *server) handleManifest(w http.ResponseWriter, req *http.Request) {
	var r ManifestReq
	if err := json.NewDecoder(req.Body).Decode(&r); err != nil {
		w.WriteHeader(http.StatusBadRequest)
	}

	if err := s.validateManifestReq(&r); err != nil {
		log.Println("validation error:", r)
		w.WriteHeader(http.StatusBadRequest)
	}

	// get narinfo

	u := url.URL{
		Scheme: "http",
		Host:   r.Upstream,
		Path:   "/" + r.StorePathHash + ".narinfo",
	}
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

	narOut := res.Body

	var decompress *exec.Cmd
	switch ni.Compression {
	case "", "none":
		decompress = nil
	case ".xz":
		decompress = exec.Command(common.XzBin, "-d")
	case ".zst":
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

	manifest, err := s.mb.Build(req.Context(), io.TeeReader(narOut, narHasher))
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

	manifest.Meta = &pb.ManifestMeta{
		NarinfoUrl:    narinfoUrl,
		Narinfo:       rawNarinfo.Bytes(),
		Generator:     common.Version,
		GeneratedTime: time.Now().Unix(),
	}

	// encode + sign manifest

	manifestBytes, err := proto.Marshal(manifest)
	if err != nil {
		log.Println("marshal error:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	signedManifest := &pb.SignedManifest{
		Manifest: manifestBytes,
	}
	if err = s.signManifest(signedManifest); err != nil {
		log.Println("sign error:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// encode and compress

	outBytes, err := proto.Marshal(signedManifest)
	if err != nil {
		log.Println("marshal error:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// TODO: write manifest itself to s3 also keyed by input

	zw, err := zstd.NewWriter(w)
	if err != nil {
		log.Println("zstd create:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = zw.Write(outBytes)
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

func (s *server) signManifest(m *pb.SignedManifest) error {
	h := sha256.New()
	h.Write(m.Manifest)
	fp := hex.EncodeToString(h.Sum(nil))
	m.HashAlgo = "sha256"
	for _, k := range s.cfg.SigningKeys {
		sig, err := k.Sign(nil, fp)
		if err != nil {
			return err
		}
		m.KeyName = append(m.KeyName, sig.Name)
		m.Signature = append(m.Signature, sig.Data)
	}
	return nil
}

func (s *server) handleChunkDiff(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
}

func (s *server) Run() error {
	mux := http.NewServeMux()
	mux.HandleFunc(ManifestPath, s.handleManifest)
	mux.HandleFunc(ChunkDiffPath, s.handleChunkDiff)

	srv := &http.Server{
		Addr:    s.cfg.Bind,
		Handler: mux,
	}
	return srv.ListenAndServe()
}
