package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/spf13/cobra"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/client"
	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/manifester"
)

func withChunkStoreWrite(c *cobra.Command) runE {
	var cscfg manifester.ChunkStoreWriteConfig

	c.Flags().StringVar(&cscfg.ChunkBucket, "chunkbucket", "", "s3 bucket to put chunks")
	c.Flags().StringVar(&cscfg.ChunkLocalDir, "chunklocaldir", "", "local directory to put chunks")
	c.Flags().IntVar(&cscfg.ZstdEncoderLevel, "chunk_store_zstd_level", 5, "encoder level for zstd chunks")

	return func(c *cobra.Command, args []string) error {
		cs, err := manifester.NewChunkStoreWrite(cscfg)
		if err != nil {
			return err
		}
		store(c, cs)
		return nil
	}
}

func withManifestBuilder(c *cobra.Command) runE {
	var mbcfg manifester.ManifestBuilderConfig

	pubkeys := c.Flags().StringArray("nix_pubkey",
		[]string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="},
		"verify narinfo with this public key")

	return chainRunE(
		withChunkStoreWrite(c),
		withSignKeys(c),
		func(c *cobra.Command, args []string) error {
			var err error
			if mbcfg.PublicKeys, err = common.LoadPubKeys(*pubkeys); err != nil {
				return err
			}
			mbcfg.SigningKeys = get[[]signature.SecretKey](c)
			cs := get[manifester.ChunkStoreWrite](c)
			mb, err := manifester.NewManifestBuilder(mbcfg, cs)
			if err != nil {
				return err
			}
			store(c, mb)
			return nil
		},
	)
}

func withDaemonConfig(c *cobra.Command) runE {
	var cfg daemon.Config

	c.Flags().StringVar(&cfg.DevPath, "devpath", "/dev/cachefiles", "path to cachefiles device node")
	c.Flags().StringVar(&cfg.CachePath, "cache", "/var/cache/styx", "path to local cache (also socket and db)")
	c.Flags().StringVar(&cfg.CacheTag, "cachetag", "styx0", "cachefiles tag")
	c.Flags().StringVar(&cfg.CacheDomain, "cachedomain", "styx", "cachefiles domain")
	c.Flags().IntVar(&cfg.ErofsBlockShift, "block_shift", 12, "block size bits for local fs images")
	// c.Flags().IntVar(&cfg.SmallFileCutoff, "small_file_cutoff", 224, "cutoff for embedding small files in images")
	c.Flags().IntVar(&cfg.Workers, "workers", 16, "worker goroutines for cachefilesd serving")
	c.Flags().IntVar(&cfg.ReadaheadChunks, "readahead_chunks", 8, "initial chunks for readahead (max 256)")

	return func(c *cobra.Command, args []string) error {
		store(c, cfg)
		return nil
	}
}

func withInitReq(c *cobra.Command) runE {
	var req daemon.InitReq

	paramsUrl := c.Flags().String("params", "", "url to read global parameters from")
	c.MarkFlagRequired("params")

	return chainRunE(
		withStyxPubKeys(c),
		func(c *cobra.Command, args []string) error {
			keys := get[[]signature.PublicKey](c)
			for _, k := range keys {
				req.PubKeys = append(req.PubKeys, k.String())
			}
			if paramsBytes, err := common.LoadFromFileOrHttpUrl(*paramsUrl); err != nil {
				return err
			} else if err = common.VerifyInlineMessage(keys, common.DaemonParamsContext, paramsBytes, &req.Params); err != nil {
				return err
			}
			store(c, &req)
			return nil
		},
	)
}

func withManifesterConfig(c *cobra.Command) runE {
	var cfg manifester.Config

	c.Flags().StringVar(&cfg.Bind, "bind", ":7420", "address to listen on")
	c.Flags().StringArrayVar(&cfg.AllowedUpstreams, "allowed-upstream",
		[]string{"cache.nixos.org"}, "allowed upstream binary caches")
	c.Flags().IntVar(&cfg.ChunkDiffZstdLevel, "chunk_diff_zstd_level", 3, "encoder level for chunk diffs")
	c.Flags().IntVar(&cfg.ChunkDiffParallel, "chunk_diff_parallel", 60, "parallelism for loading chunks for diff")

	return func(c *cobra.Command, args []string) error {
		store(c, cfg)
		return nil
	}
}

func withSignKeys(c *cobra.Command) runE {
	signkeys := c.Flags().StringArray("styx_signkey", nil, "sign manifest with key from this file")
	ssmsignkey := c.Flags().StringArray("styx_ssm_signkey", nil, "sign manifest with key from SSM")
	return func(c *cobra.Command, args []string) error {
		var keys []signature.SecretKey
		var err error
		if len(*ssmsignkey) > 0 {
			keys, err = loadKeysFromSsm(*ssmsignkey)
		} else {
			keys, err = common.LoadSecretKeys(*signkeys)
		}
		if err != nil {
			return err
		}
		store(c, keys)
		return nil
	}
}

func withStyxPubKeys(c *cobra.Command) runE {
	pubkeys := c.Flags().StringArray("styx_pubkey", nil, "verify params and manifests with this key")
	return func(c *cobra.Command, args []string) error {
		keys, err := common.LoadPubKeys(*pubkeys)
		if err != nil {
			return err
		}
		store(c, keys)
		return nil
	}
}

func withStyxClient(c *cobra.Command) runE {
	socket := c.Flags().String("addr", "/var/cache/styx/styx.sock", "path to local styx socket")
	return func(c *cobra.Command, args []string) error {
		store(c, client.NewClient(*socket))
		return nil
	}
}

func withDebugReq(c *cobra.Command) runE {
	var req daemon.DebugReq
	c.Flags().BoolVar(&req.IncludeAllImages, "all-images", false, "include all images")
	c.Flags().StringArrayVarP(&req.IncludeImages, "image", "i", nil, "specific images to include")
	c.Flags().BoolVar(&req.IncludeChunks, "chunks", false, "include all chunks")
	c.Flags().BoolVar(&req.IncludeSlabs, "slabs", false, "include slabs")
	return func(c *cobra.Command, args []string) error {
		for i, img := range req.IncludeImages {
			img = strings.TrimPrefix(img, "/nix/store/")
			img, _, _ = strings.Cut(img, "-")
			req.IncludeImages[i] = img
		}
		store(c, &req)
		return nil
	}
}

func withRepairReq(c *cobra.Command) runE {
	var req daemon.RepairReq
	c.Flags().BoolVar(&req.Presence, "presence", false, "repair presence info")
	return func(c *cobra.Command, args []string) error {
		store(c, &req)
		return nil
	}
}

func main() {
	if os.Getenv("NOTIFY_SOCKET") != "" || os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		// running in systemd or on lambda
		log.SetFlags(log.Lshortfile)
	} else {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}

	root := cmd(
		&cobra.Command{
			Use:   "styx",
			Short: "styx - streaming filesystem for nix",
		},
		cmd(
			&cobra.Command{Use: "daemon", Short: "act as local daemon"},
			withDaemonConfig,
			func(c *cobra.Command, args []string) error {
				err := daemon.CachefilesServer(get[daemon.Config](c)).Start()
				if err != nil {
					return err
				}
				return <-make(chan error) // block forever
			},
		),
		cmd(
			&cobra.Command{
				Use:   "manifester",
				Short: "act as manifester server",
			},
			withManifesterConfig,
			withManifestBuilder,
			func(c *cobra.Command, args []string) error {
				cfg := get[manifester.Config](c)
				mb := get[*manifester.ManifestBuilder](c)
				m, err := manifester.NewManifestServer(cfg, mb)
				if err != nil {
					return err
				}
				return m.Run()
			},
		),
		cmd(
			&cobra.Command{
				Use:   "init",
				Short: "initializes daemon with required configuration (client)",
			},
			withStyxClient,
			withInitReq,
			func(c *cobra.Command, args []string) error {
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.InitPath, get[*daemon.InitReq](c))
			},
		),
		cmd(
			&cobra.Command{
				Use:   "mount <upstream> <store path> <mount point>",
				Short: "mounts a nix package (client)",
				Args:  cobra.ExactArgs(3),
			},
			withStyxClient,
			func(c *cobra.Command, args []string) error {
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.MountPath, &daemon.MountReq{
						Upstream:   args[0],
						StorePath:  args[1],
						MountPoint: args[2],
					},
				)
			},
		),
		cmd(
			&cobra.Command{
				Use:   "umount <store path>",
				Short: "unmounts a nix package that was previously mounted (client)",
				Args:  cobra.ExactArgs(1),
			},
			withStyxClient,
			func(c *cobra.Command, args []string) error {
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.UmountPath, &daemon.UmountReq{
						StorePath: args[0],
					},
				)
			},
		),
		cmd(
			&cobra.Command{
				Use:   "prefetch <path>",
				Short: "prefetch the given file or directory (client)",
				Args:  cobra.ExactArgs(1),
			},
			withStyxClient,
			func(c *cobra.Command, args []string) error {
				arg, err := filepath.Abs(args[0])
				if err != nil {
					return err
				}
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.PrefetchPath, &daemon.PrefetchReq{
						Path: arg,
					},
				)
			},
		),
		cmd(
			&cobra.Command{
				Use:   "debug",
				Short: "dumps info from daemon (client)",
			},
			withStyxClient,
			withDebugReq,
			func(c *cobra.Command, args []string) error {
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.DebugPath, get[*daemon.DebugReq](c))
			},
		),
		cmd(
			&cobra.Command{
				Use:   "repair",
				Short: "tries to repair bad states (client)",
			},
			withStyxClient,
			withRepairReq,
			func(c *cobra.Command, args []string) error {
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.RepairPath, get[*daemon.RepairReq](c))
			},
		),
		internalCmd(),
	)
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}
