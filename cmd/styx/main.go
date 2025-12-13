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
	"github.com/dnr/styx/common/systemd"
	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
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
	cfg := daemon.Config{
		FdStore: systemd.SystemdFdStore{},
	}

	c.Flags().StringVar(&cfg.DevPath, "devpath", "/dev/cachefiles", "path to cachefiles device node")
	c.Flags().StringVar(&cfg.CachePath, "cache", "/var/cache/styx", "path to local cache (also socket and db)")
	c.Flags().StringVar(&cfg.CacheTag, "cachetag", "styx0", "cachefiles tag")
	c.Flags().StringVar(&cfg.CacheDomain, "cachedomain", "styx", "cachefiles domain")
	c.Flags().IntVar(&cfg.ErofsBlockShift, "block_shift", 12, "block size bits for local fs images")
	// c.Flags().IntVar(&cfg.SmallFileCutoff, "small_file_cutoff", 224, "cutoff for embedding small files in images")
	c.Flags().IntVar(&cfg.Workers, "workers", 16, "worker goroutines for cachefilesd serving")

	return storer(&cfg)
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
	c.Flags().StringArrayVar(&cfg.AllowedUpstreams, "allowed_upstream",
		[]string{
			"cache.nixos.org",
			"releases.nixos.org",
			"channels.nixos.org",
		}, "allowed upstream binary caches or tarball sources")
	c.Flags().IntVar(&cfg.ChunkDiffZstdLevel, "chunk_diff_zstd_level", 3, "encoder level for chunk diffs")
	c.Flags().IntVar(&cfg.ChunkDiffParallel, "chunk_diff_parallel", 60, "parallelism for loading chunks for diff")

	return storer(&cfg)
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
	return storer(client.NewClient(*socket))
}

func withVaporizeReq(c *cobra.Command) runE {
	var req daemon.VaporizeReq
	c.Flags().StringVar(&req.Name, "name", "", "store name, if not same as path basename")
	return func(c *cobra.Command, args []string) error {
		req.Path = args[0]
		store(c, &req)
		return nil
	}
}

func withGcReq(c *cobra.Command) runE {
	var req daemon.GcReq
	dryRunSlow := c.Flags().Bool("with_freed_stats", false, "return stats on bytes freed too")
	doIt := c.Flags().Bool("doit", false, "actually do the gc")
	includeUnmounted := c.Flags().Bool("unmounted", true, "gc unmounted images")
	includeMaterialized := c.Flags().Bool("materialized", false, "gc materialized images too (only affect slab)")
	includeErrorStates := c.Flags().Bool("error_states", false, "gc images in error states too")
	return func(c *cobra.Command, args []string) error {
		if *doIt {
			// leave fields false
		} else if *dryRunSlow {
			req.DryRunSlow = true
		} else {
			req.DryRunFast = true
		}
		req.GcByState = make(map[pb.MountState]bool)
		if *includeUnmounted {
			req.GcByState[pb.MountState_Unmounted] = true
		}
		if *includeMaterialized {
			req.GcByState[pb.MountState_Materialized] = true
		}
		if *includeErrorStates {
			req.GcByState[pb.MountState_Unknown] = true
			req.GcByState[pb.MountState_Requested] = true
			req.GcByState[pb.MountState_MountError] = true
			req.GcByState[pb.MountState_UnmountRequested] = true
			req.GcByState[pb.MountState_Deleted] = true
		}
		store(c, &req)
		return nil
	}
}

func withDebugReq(c *cobra.Command) runE {
	var req daemon.DebugReq
	c.Flags().BoolVar(&req.IncludeAllImages, "all-images", false, "include all images")
	c.Flags().StringArrayVarP(&req.IncludeImages, "image", "i", nil, "specific images to include")
	c.Flags().BoolVar(&req.IncludeAllChunks, "all-chunks", false, "include all chunks")
	c.Flags().StringArrayVarP(&req.IncludeChunks, "chunk", "c", nil, "specific chunks to include")
	c.Flags().BoolVar(&req.IncludeChunkSharing, "chunk-sharing", false, "chunk sharing distribution")
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
	return storer(&req)
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
				err := daemon.NewServer(*get[*daemon.Config](c)).Start()
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
				cfg := get[*manifester.Config](c)
				mb := get[*manifester.ManifestBuilder](c)
				m, err := manifester.NewManifestServer(*cfg, mb)
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
				Use:   "materialize <upstream> <store path> <dest>",
				Short: "downloads a nix package to a regular filesystem (client)",
				Args:  cobra.ExactArgs(3),
			},
			withStyxClient,
			func(c *cobra.Command, args []string) error {
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.MaterializePath, &daemon.MaterializeReq{
						Upstream:  args[0],
						StorePath: args[1],
						DestPath:  args[2],
					},
				)
			},
		),
		cmd(
			&cobra.Command{
				Use:   "vaporize [--name] <path>",
				Short: "imports data from the local filesystem into styx (client)",
				Args:  cobra.ExactArgs(1),
			},
			withStyxClient,
			withVaporizeReq,
			func(c *cobra.Command, args []string) error {
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.VaporizePath, get[*daemon.VaporizeReq](c),
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
		tarballCmd,
		cmd(
			&cobra.Command{
				Use:   "gc",
				Short: "garbage collects the styx store",
			},
			withStyxClient,
			withGcReq,
			func(c *cobra.Command, args []string) error {
				return get[*client.StyxClient](c).CallAndPrint(
					daemon.GcPath, get[*daemon.GcReq](c),
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
