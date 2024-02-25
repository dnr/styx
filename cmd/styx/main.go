package main

import (
	"context"
	"log"
	"os"

	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/spf13/cobra"

	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/manifester"
)

const (
	ctxChunkStore = iota
	ctxManifestBuilder
	ctxDaemonConfig
	ctxManifesterConfig
	ctxInFile
	ctxOutFile
)

func withChunkStore(c *cobra.Command) runE {
	var cscfg manifester.ChunkStoreConfig

	c.Flags().StringVar(&cscfg.ChunkBucket, "chunkbucket", "", "s3 bucket to put chunks and cached manifests")
	c.Flags().StringVar(&cscfg.ChunkLocalDir, "chunklocaldir", "", "local directory to put chunks")

	return func(c *cobra.Command, args []string) error {
		cs, err := manifester.NewChunkStore(cscfg)
		if err != nil {
			return err
		}
		c.SetContext(context.WithValue(c.Context(), ctxChunkStore, cs))
		return nil
	}
}

func withManifestBuilder(c *cobra.Command) runE {
	var mbcfg manifester.ManifestBuilderConfig
	return chainRunE(
		withChunkStore(c),
		func(c *cobra.Command, args []string) error {
			cs := c.Context().Value(ctxChunkStore).(manifester.ChunkStore)
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

	c.Flags().StringVar(&cfg.DevPath, "devpath", "/dev/cachefiles", "path to cachefiles device node")
	c.Flags().StringVar(&cfg.CachePath, "cache", "/var/cache/styx", "path to local cache (also socket and db)")

	return func(c *cobra.Command, args []string) error {
		c.SetContext(context.WithValue(c.Context(), ctxDaemonConfig, cfg))
		return nil
	}
}

func withManifesterConfig(c *cobra.Command) runE {
	var cfg manifester.Config

	c.Flags().StringVar(&cfg.Bind, "bind", ":7420", "address to listen on")
	c.Flags().StringArrayVar(&cfg.AllowedUpstreams, "allowed-upstream", []string{"cache.nixos.org"}, "allowed upstream binary caches")
	pubkeys := c.Flags().StringArray("pubkey",
		[]string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="},
		"verify narinfo with this public key")
	signkeys := c.Flags().StringArray("signkey", nil, "sign manifest with key from this file")

	return chainRunE(
		withManifestBuilder(c),
		func(c *cobra.Command, args []string) error {
			for _, pk := range *pubkeys {
				if k, err := signature.ParsePublicKey(pk); err != nil {
					return err
				} else {
					cfg.PublicKeys = append(cfg.PublicKeys, k)
				}
			}
			for _, path := range *signkeys {
				if skdata, err := os.ReadFile(path); err != nil {
					return err
				} else if k, err := signature.LoadSecretKey(string(skdata)); err != nil {
					return err
				} else {
					cfg.SigningKeys = append(cfg.SigningKeys, k)
				}
			}
			cfg.ManifestBuilder = c.Context().Value(ctxManifestBuilder).(*manifester.ManifestBuilder)
			c.SetContext(context.WithValue(c.Context(), ctxManifesterConfig, cfg))
			return nil
		},
	)
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
