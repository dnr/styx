package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"google.golang.org/protobuf/proto"
)

// local binary cache to serve narinfo

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
	} else if data, ok := s.fakeBinaryCache.Get(m[1]); !ok {
		http.NotFound(w, r)
	} else {
		w.Header().Set("Content-Type", "text/x-nix-narinfo")
		w.Write(data.narinfo)
	}
}

// request handler

func (s *Server) handleTarballReq(ctx context.Context, r *TarballReq) (*TarballResp, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}

	// use generic tarball build mode
	mReq := manifester.ManifestReq{
		Upstream:   r.UpstreamUrl,
		BuildMode:  manifester.ModeGenericTarball,
		DigestAlgo: cdig.Algo,
		DigestBits: int(cdig.Bits),
		// SmallFileCutoff: s.cfg.SmallFileCutoff,
	}
	envelopeBytes, err := s.getNewManifest(ctx, mReq, 0)
	if err != nil {
		return nil, err
	}

	// verify signature and params
	entry, smParams, err := common.VerifyMessageAsEntry(s.p().keys, common.ManifestContext, envelopeBytes)
	if err != nil {
		return nil, err
	} else if smParams.GetDigestBits() != cdig.Bits || smParams.GetDigestAlgo() != cdig.Algo {
		return nil, fmt.Errorf("chunked manifest global params mismatch")
	}

	// TODO: we should check the envelope context against what we asked for. but we don't know
	// the sph of what we asked for, and we don't even know the resolved url. so we can't
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
	// TODO: prune cache once in a while
	s.fakeBinaryCache.Put(sph, binaryCacheData{
		narinfo:  []byte(ni.String()),
		upstream: mm.GenericTarballResolved,
	})

	return &TarballResp{
		ResolvedUrl:   mm.GenericTarballResolved,
		StorePathHash: sph,
		Name:          nipb.StorePath[44:],
		NarHash:       hex.EncodeToString(nh.Digest()),
		NarHashAlgo:   nh.HashTypeString(),
	}, nil
}
