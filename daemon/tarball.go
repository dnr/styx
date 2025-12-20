package daemon

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

// local binary cache to serve narinfo

var errNotFound = errors.New("not found")

func (s *Server) getFakeCacheData(sphStr string) (*pb.FakeCacheData, error) {
	var data pb.FakeCacheData
	err := s.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(fakeCacheBucket).Get([]byte(sphStr))
		if v == nil {
			return errNotFound
		}
		return proto.Unmarshal(v, &data)
	})
	return common.ValOrErr(&data, err)
}

func (s *Server) startFakeCacheServer() (err error) {
	l, err := net.Listen("tcp", fakeCacheBind)
	if err != nil {
		return fmt.Errorf("failed to listen on local tcp socket %s: %w", fakeCacheBind, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/nix-cache-info", s.getFakeCacheInfo)
	mux.HandleFunc("/", s.getFakeNarinfo)
	s.shutdownWait.Add(1)
	go func() {
		defer s.shutdownWait.Done()
		srv := &http.Server{Handler: mux}
		go srv.Serve(l)
		<-s.shutdownChan
		log.Printf("stopping fake cache server")
		srv.Close()
	}()
	return nil
}

func (s *Server) getFakeCacheInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/x-nix-cache-info")
	w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 0\nPriority: 90\n"))
}

var reNarinfoPath = regexp.MustCompile(`^/([` + nixbase32.Alphabet + `]+)\.narinfo$`)

func (s *Server) getFakeNarinfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	} else if m := reNarinfoPath.FindStringSubmatch(r.URL.Path); m == nil {
		http.NotFound(w, r)
	} else if data, err := s.getFakeCacheData(m[1]); err != nil {
		http.NotFound(w, r)
	} else {
		w.Header().Set("Content-Type", "text/x-nix-narinfo")
		w.Write(data.Narinfo)
	}
}

// request handler

func (s *Server) handleTarballReq(ctx context.Context, r *TarballReq) (*TarballResp, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}

	// we only have a url at this point, not a sph. the url may redirect to a more permanent
	// url. do a head request to resolve and get at least an etag if possible.
	// TODO: consider "lockable tarball protocol"
	res, err := common.RetryHttpRequest(ctx, http.MethodHead, r.UpstreamUrl, "", nil)
	if err != nil {
		return nil, err
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	resolved := res.Request.URL.String()

	// use generic tarball build mode
	mReq := manifester.ManifestReq{
		Upstream:   resolved,
		BuildMode:  manifester.ModeGenericTarball,
		DigestAlgo: cdig.Algo,
		DigestBits: int(cdig.Bits),
	}

	var envelopeBytes []byte

	// if we have an etag we can try a cache lookup
	if etag := res.Header.Get("Etag"); etag != "" {
		// we have an etag, we can try a cache lookup
		mReq.ETag = etag
		envelopeBytes, _ = s.p().mcread.Get(ctx, mReq.CacheKey(), nil)
		mReq.ETag = ""
	}

	// fall back to asking manifester
	if envelopeBytes == nil {
		envelopeBytes, err = s.getNewManifest(ctx, mReq, r.Shards)
		if err != nil {
			return nil, err
		}
	}

	// verify signature and params
	entry, _, err := common.VerifyMessageAsEntry(s.p().keys, common.ManifestContext, envelopeBytes)
	if err != nil {
		return nil, err
	}

	// TODO: we should check the envelope context against what we asked for, but we don't know
	// the sph of what we asked for, we either know an etag (md5sum) or nothing. so we can't
	// really check any more than the signature above.

	// get the narinfo that the manifester produced so we can add it to our fake cache.
	// try embedded meta in entry first (will be present on chunked manifests).
	mm := entry.ManifestMeta
	if mm == nil && len(entry.InlineData) > 0 {
		// manifest is inline, use that
		var m pb.Manifest
		err = proto.Unmarshal(entry.InlineData, &m)
		if err != nil {
			return nil, fmt.Errorf("manifest unmarshal error: %w", err)
		}
		mm = m.Meta
	}
	nipb := mm.GetNarinfo()
	if nipb == nil {
		return nil, fmt.Errorf("entry missing inline manifest metadata")
	}

	fh, err := hash.ParseNixBase32(nipb.FileHash)
	if err != nil {
		return nil, err
	}
	nh, err := hash.ParseNixBase32(nipb.NarHash)
	if err != nil {
		return nil, err
	}

	ni := narinfo.NarInfo{
		StorePath:   nipb.StorePath,
		URL:         "nar/dummy.nar", // we'll use styx
		Compression: "none",
		FileHash:    fh,
		FileSize:    uint64(nipb.FileSize),
		NarHash:     nh,
		NarSize:     uint64(nipb.NarSize),
		// needed to make nix treat it as CA so doesn't it require a signature
		CA: "fixed:r:" + nh.NixString(),
	}

	sph := nipb.StorePath[11:43]
	b, err := proto.Marshal(&pb.FakeCacheData{
		Narinfo:  []byte(ni.String()),
		Upstream: mm.GenericTarballResolved,
		Updated:  time.Now().Unix(),
	})
	if err != nil {
		return nil, err
	}
	// TODO: prune this cache once in a while
	err = s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(fakeCacheBucket).Put([]byte(sph), b)
	})
	if err != nil {
		return nil, err
	}

	return &TarballResp{
		ResolvedUrl:   mm.GenericTarballResolved,
		StorePathHash: sph,
		Name:          nipb.StorePath[44:],
		NarHash:       hex.EncodeToString(nh.Digest()),
		NarHashAlgo:   nh.HashTypeString(),
	}, nil
}
