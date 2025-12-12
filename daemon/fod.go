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

func (s *Server) startFodCacheServer() (err error) {
	l, err := net.Listen("tcp", fodCacheBind)
	if err != nil {
		return fmt.Errorf("failed to listen on local tcp socket %s: %w", fodCacheBind, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/nix-cache-info", s.getFodCacheInfo)
	mux.HandleFunc("/", s.getFodNarinfo)
	s.shutdownWait.Add(1)
	go func() {
		defer s.shutdownWait.Done()
		srv := &http.Server{Handler: mux}
		go srv.Serve(l)
		<-s.shutdownChan
		log.Printf("stopping fod cache server")
		srv.Close()
	}()
	return nil
}

func (s *Server) getFodCacheInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/x-nix-cache-info")
	w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 0\nPriority: 90\n"))
}

var reNarinfoPath = regexp.MustCompile(`^/([` + nixbase32.Alphabet + `]+)\.narinfo$`)

func (s *Server) getFodNarinfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	} else if m := reNarinfoPath.FindStringSubmatch(r.URL.Path); m == nil {
		http.NotFound(w, r)
	} else if ni, ok := s.fodNarinfo.Get(m[1]); !ok {
		http.NotFound(w, r)
	} else {
		w.Header().Set("Content-Type", "text/x-nix-narinfo")
		w.Write(ni)
	}
}

// request handler

func (s *Server) handleGenericFodReq(ctx context.Context, r *GenericFodReq) (*GenericFodResp, error) {
	if s.p() == nil {
		return nil, mwErr(http.StatusPreconditionFailed, "styx is not initialized, call 'styx init --params=...'")
	}

	// use generic tarball manifest mode
	envelopeBytes, err := s.getManifestFromManifester(ctx, r.UpstreamUrl, manifester.SphGenericTarball, 0)
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

	// FIXME: verify something here?
	// // check entry path to get storepath
	// storePath := strings.TrimPrefix(entry.Path, common.ManifestContext+"/")
	// if storePath != req.StorePath {
	// 	return nil, nil, fmt.Errorf("envelope storepath != requested storepath: %q != %q", storePath, req.StorePath)
	// }

	// try embedded meta in entry first
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
	}

	sph := nipb.StorePath[11:43]
	s.fodNarinfo.Put(sph, []byte(ni.String()))

	return &GenericFodResp{
		ResolvedUrl:   mm.GenericTarballResolved,
		StorePathHash: sph,
		Name:          nipb.StorePath[44:],
		NarHash:       hex.EncodeToString(nh.Digest()),
		NarHashAlgo:   nh.HashTypeString(),
	}, nil
}
