package main

import (
	"context"
	"log"

	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/spf13/cobra"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/manifester"
)

const (
	ctxChunkStoreWrite = iota
	ctxChunkStoreRead  // FIXME
	ctxManifestBuilder
	ctxDaemonConfig
	ctxManifesterConfig
	ctxInFile
	ctxOutFile
	ctxSignKeys
	ctxStyxPubKeys
)

func withChunkStoreWrite(c *cobra.Command) runE {
	var cscfg manifester.ChunkStoreWriteConfig

	c.Flags().StringVar(&cscfg.ChunkBucket, "chunkbucket", "", "s3 bucket to put chunks")
	c.Flags().StringVar(&cscfg.ChunkLocalDir, "chunklocaldir", "", "local directory to put chunks")

	return func(c *cobra.Command, args []string) error {
		cs, err := manifester.NewChunkStoreWrite(cscfg)
		if err != nil {
			return err
		}
		c.SetContext(context.WithValue(c.Context(), ctxChunkStoreWrite, cs))
		return nil
	}
}

// FIXME
func withChunkStoreRead(c *cobra.Command) runE {
	var cscfg manifester.ChunkStoreReadConfig

	c.Flags().StringVar(&cscfg.ChunkBucket, "rchunkbucket", "", "s3 bucket to get chunks")
	c.Flags().StringVar(&cscfg.ChunkLocalDir, "rchunklocaldir", "", "local directory to read chunks from")

	return func(c *cobra.Command, args []string) error {
		cs, err := manifester.NewChunkStoreRead(cscfg)
		if err != nil {
			return err
		}
		c.SetContext(context.WithValue(c.Context(), ctxChunkStoreRead, cs))
		return nil
	}
}

func withManifestBuilder(c *cobra.Command) runE {
	var mbcfg manifester.ManifestBuilderConfig
	return chainRunE(
		withChunkStoreWrite(c),
		func(c *cobra.Command, args []string) error {
			cs := c.Context().Value(ctxChunkStoreWrite).(manifester.ChunkStoreWrite)
			mb, err := manifester.NewManifestBuilder(mbcfg, cs)
			if err != nil {
				return err
			}
			c.SetContext(context.WithValue(c.Context(), ctxManifestBuilder, mb))
			return nil
		},
	)
}

func withDaemonConfig(c *cobra.Command) runE {
	var cfg daemon.Config

	paramsUrl := c.Flags().String("params", "", "url to read global parameters from")
	c.MarkFlagRequired("params")
	c.Flags().StringVar(&cfg.DevPath, "devpath", "/dev/cachefiles", "path to cachefiles device node")
	c.Flags().StringVar(&cfg.CachePath, "cache", "/var/cache/styx", "path to local cache (also socket and db)")
	c.Flags().StringVar(&cfg.Upstream, "upstream", "cache.nixos.org", "upstream cache to ask manifester for")
	c.Flags().IntVar(&cfg.ErofsBlockShift, "block_shift", 12, "block size bits for local fs images")
	c.Flags().IntVar(&cfg.SmallFileCutoff, "small_file_cutoff", 224, "cutoff for embedding small files in images")
	c.Flags().IntVar(&cfg.Workers, "workers", 16, "worker goroutines for cachefilesd serving")

	return chainRunE(
		withStyxPubKeys(c),
		func(c *cobra.Command, args []string) error {
			cfg.StyxPubKeys = c.Context().Value(ctxStyxPubKeys).([]signature.PublicKey)
			if paramsBytes, err := common.LoadFromFileOrHttpUrl(*paramsUrl); err != nil {
				return err
			} else if err = common.VerifyMessage(cfg.StyxPubKeys, paramsBytes, &cfg.Params); err != nil {
				return err
			}
			// FIXME: &cfg
			c.SetContext(context.WithValue(c.Context(), ctxDaemonConfig, cfg))
			return nil
		},
	)
}

func withManifesterConfig(c *cobra.Command) runE {
	var cfg manifester.Config

	c.Flags().StringVar(&cfg.Bind, "bind", ":7420", "address to listen on")
	c.Flags().StringArrayVar(&cfg.AllowedUpstreams, "allowed-upstream",
		[]string{"cache.nixos.org"}, "allowed upstream binary caches")
	pubkeys := c.Flags().StringArray("nix_pubkey",
		[]string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="},
		"verify narinfo with this public key")

	return chainRunE(
		withSignKeys(c),
		withManifestBuilder(c),
		func(c *cobra.Command, args []string) error {
			var err error
			if cfg.PublicKeys, err = common.LoadPubKeys(*pubkeys); err != nil {
				return err
			}
			cfg.SigningKeys = c.Context().Value(ctxSignKeys).([]signature.SecretKey)
			cfg.ManifestBuilder = c.Context().Value(ctxManifestBuilder).(*manifester.ManifestBuilder)
			c.SetContext(context.WithValue(c.Context(), ctxManifesterConfig, cfg))
			return nil
		},
	)
}

func withSignKeys(c *cobra.Command) runE {
	signkeys := c.Flags().StringArray("styx_signkey", nil, "sign manifest with key from this file")
	return func(c *cobra.Command, args []string) error {
		keys, err := common.LoadSecretKeys(*signkeys)
		if err != nil {
			return err
		}
		c.SetContext(context.WithValue(c.Context(), ctxSignKeys, keys))
		return nil
	}
}

func withStyxPubKeys(c *cobra.Command) runE {
	pubkeys := c.Flags().StringArray("styx_pubkey",
		[]string{"styx-dev-1:SCMYzQjLTMMuy/MlovgOX0rRVCYKOj+cYAfQrqzcLu0="},
		"verify params and manifests with this key")
	return func(c *cobra.Command, args []string) error {
		keys, err := common.LoadPubKeys(*pubkeys)
		if err != nil {
			return err
		}
		c.SetContext(context.WithValue(c.Context(), ctxStyxPubKeys, keys))
		return nil
	}
}

func main() {
	root := cmd(
		&cobra.Command{
			Use:   "styx",
			Short: "styx - streaming filesystem for nix",
		},
		cmd(
			&cobra.Command{Use: "daemon", Short: "act as local daemon"},
			withDaemonConfig,
			func(c *cobra.Command, args []string) error {
				cfg := c.Context().Value(ctxDaemonConfig).(daemon.Config)
				return daemon.CachefilesServer(cfg).Run()
			},
		),
		cmd(
			&cobra.Command{
				Use:   "manifester",
				Short: "act as manifester server",
			},
			withManifesterConfig,
			func(c *cobra.Command, args []string) error {
				cfg := c.Context().Value(ctxManifesterConfig).(manifester.Config)
				m, err := manifester.ManifestServer(cfg)
				if err != nil {
					return err
				}
				return m.Run()
			},
		),
		cmd(
			&cobra.Command{
				Use:     "client",
				Aliases: []string{"c"},
				Short:   "client to local daemon",
			},
			cmd(
				&cobra.Command{
					Use:   "mount <nar file or store path> <mount point>",
					Short: "mounts a nix package",
					Args:  cobra.ExactArgs(2),
				},
				func(c *cobra.Command, args []string) error { panic("TODO") },
			),
		),
		debugCmd(),
	)
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}
