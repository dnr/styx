package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/cdig"
	"github.com/dnr/styx/common/cobrautil"
	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
)

type (
	remanifestReq struct {
		cacheurl   string
		requrl     string
		reqIfFound bool
		reqIfNot   bool
		upstream   string
		storePath  string
	}
)

func withInFile(c *cobra.Command, args []string) error {
	in, err := os.Open(args[0])
	if err != nil {
		return err
	}
	cobrautil.StoreKeyed(c, in, "in")
	c.PostRunE = cobrautil.Chain(c.PostRunE, in.Close)
	return nil
}

func withOutFile(c *cobra.Command, args []string) error {
	out, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	cobrautil.StoreKeyed(c, out, "out")
	c.PostRunE = cobrautil.Chain(c.PostRunE, out.Close)
	return nil
}

func withRemanifestReq(c *cobra.Command) *remanifestReq {
	var req remanifestReq
	c.Flags().StringVar(&req.cacheurl, "cacheurl", "", "manifest cache url")
	c.MarkFlagRequired("cacheurl")
	c.Flags().StringVar(&req.requrl, "requrl", "", "manifester request url")
	c.MarkFlagRequired("requrl")
	c.Flags().BoolVar(&req.reqIfFound, "request_if_found", false, "rerequest if found")
	c.Flags().BoolVar(&req.reqIfNot, "request_if_not", false, "rerequest if not found")
	c.Flags().StringVar(&req.upstream, "upstream", "https://cache.nixos.org/", "remanifest upstream")
	c.Flags().StringVar(&req.storePath, "storepath", "", "remanifest store path")
	return &req
}

func internalCmd() *cobra.Command {
	return cobrautil.Cmd(
		&cobra.Command{Use: "internal", Short: "internal commands"},
		cobrautil.Cmd(
			&cobra.Command{
				Use:  "signdaemonparams <daemon params json> <out file image>",
				Args: cobra.ExactArgs(2),
			},
			withSignKeys,
			withInFile,
			withOutFile,
			func(c *cobra.Command, keys []signature.SecretKey) error {
				in := cobrautil.GetKeyed[*os.File](c, "in")
				out := cobrautil.GetKeyed[*os.File](c, "out")
				var params pb.DaemonParams
				var sb []byte
				if b, err := io.ReadAll(in); err != nil {
					return err
				} else if err = protojson.Unmarshal(b, &params); err != nil {
					return err
				} else if sb, err = common.SignInlineMessage(keys, common.DaemonParamsContext, &params); err != nil {
					return err
				} else if _, err = out.Write(sb); err != nil {
					return err
				}
				return nil
			},
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "remanifest",
				Short: "re-request manifests",
			},
			withRemanifestReq,
			func(c *cobra.Command, req *remanifestReq) error {
				_, sphStr, _ := daemon.ParseSph(req.storePath)
				mReq := manifester.ManifestReq{
					Upstream:      req.upstream,
					StorePathHash: sphStr,
					DigestAlgo:    cdig.Algo,
					DigestBits:    int(cdig.Bits),
				}

				mcread := manifester.NewChunkStoreReadUrl(req.cacheurl, manifester.ManifestCachePath)

				doRequest := false
				if _, err := mcread.Get(c.Context(), mReq.CacheKey(), nil); err == nil {
					log.Println("manifest cache hit for", sphStr)
					doRequest = req.reqIfFound
				} else {
					log.Println("manifest not found for", sphStr, err)
					doRequest = req.reqIfNot
				}

				if !doRequest {
					return nil
				}

				reqBytes, err := json.Marshal(mReq)
				if err != nil {
					return err
				}

				u := strings.TrimSuffix(req.requrl, "/") + manifester.ManifestPath
				hReq, err := http.NewRequestWithContext(c.Context(), http.MethodPost, u, bytes.NewReader(reqBytes))
				if err != nil {
					return err
				}
				hReq.Header.Set("Content-Type", common.CTJson)
				res, err := http.DefaultClient.Do(hReq)
				if err == nil {
					if res.StatusCode != http.StatusOK {
						err = common.HttpErrorFromRes(res)
					}
					res.Body.Close()
				}
				if err == nil {
					log.Println("manifest request for", sphStr, "success")
				} else {
					log.Println("manifest request for", sphStr, "failed", err)
				}
				return nil
			},
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "mcachekey",
				Short: "print manifest cache key",
				Args:  cobra.ExactArgs(2),
			},
			func(c *cobra.Command, args []string) error {
				mReq := manifester.ManifestReq{
					Upstream:      args[0],
					StorePathHash: args[1],
					DigestAlgo:    cdig.Algo,
					DigestBits:    int(cdig.Bits),
				}
				log.Printf("req: %#v\nkey: %s\n", mReq, mReq.CacheKey())
				return nil
			},
		),
	)
}
