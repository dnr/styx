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
	storeKeyed(c, in, "in")
	c.PostRunE = chainRunE(c.PostRunE, func(c *cobra.Command, args []string) error {
		return in.Close()
	})
	return nil
}

func withOutFile(c *cobra.Command, args []string) error {
	out, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	storeKeyed(c, out, "out")
	c.PostRunE = chainRunE(c.PostRunE, func(c *cobra.Command, args []string) error {
		return out.Close()
	})
	return nil
}

func withRemanifestReq(c *cobra.Command) runE {
	var req remanifestReq
	c.Flags().StringVar(&req.cacheurl, "cacheurl", "", "manifest cache url")
	c.MarkFlagRequired("cacheurl")
	c.Flags().StringVar(&req.requrl, "requrl", "", "manifester request url")
	c.MarkFlagRequired("requrl")
	c.Flags().BoolVar(&req.reqIfFound, "request_if_found", false, "rerequest if found")
	c.Flags().BoolVar(&req.reqIfNot, "request_if_not", false, "rerequest if not found")
	c.Flags().StringVar(&req.upstream, "upstream", "https://cache.nixos.org/", "remanifest upstream")
	c.Flags().StringVar(&req.storePath, "storepath", "", "remanifest store path")
	return func(c *cobra.Command, args []string) error {
		store(c, &req)
		return nil
	}
}

func internalCmd() *cobra.Command {
	return cmd(
		&cobra.Command{Use: "internal", Short: "internal commands"},
		cmd(
			&cobra.Command{
				Use:  "signdaemonparams <daemon params json> <out file image>",
				Args: cobra.ExactArgs(2),
			},
			withSignKeys,
			withInFile,
			withOutFile,
			func(c *cobra.Command, args []string) error {
				in := getKeyed[*os.File](c, "in")
				out := getKeyed[*os.File](c, "out")
				keys := get[[]signature.SecretKey](c)
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
		cmd(
			&cobra.Command{
				Use:   "remanifest",
				Short: "re-request manifests",
			},
			withRemanifestReq,
			func(c *cobra.Command, args []string) error {
				req := get[*remanifestReq](c)
				sph, _, _ := strings.Cut(req.storePath, "-")
				mReq := manifester.ManifestReq{
					Upstream:      req.upstream,
					StorePathHash: sph,
					ChunkShift:    int(common.ChunkShift),
					DigestAlgo:    common.DigestAlgo,
					DigestBits:    int(cdig.Bits),
				}

				mcread := manifester.NewChunkStoreReadUrl(req.cacheurl, manifester.ManifestCachePath)

				doRequest := false
				if _, err := mcread.Get(c.Context(), mReq.CacheKey(), nil); err == nil {
					log.Println("manifest cache hit for", sph)
					doRequest = req.reqIfFound
				} else {
					log.Println("manifest not found for", sph, err)
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
				hReq.Header.Set("Content-Type", "application/json")
				res, err := http.DefaultClient.Do(hReq)
				if err == nil {
					if res.StatusCode != http.StatusOK {
						err = common.HttpError(res.StatusCode)
					}
					res.Body.Close()
				}
				if err == nil {
					log.Println("manifest request for", sph, "success")
				} else {
					log.Println("manifest request for", sph, "failed", err)
				}
				return nil
			},
		),
	)
}
