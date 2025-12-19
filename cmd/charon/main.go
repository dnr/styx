package main

import (
	"fmt"
	"log"
	"log/slog"
	"time"

	axiom_slog_adapter "github.com/axiomhq/axiom-go/adapters/slog"
	"github.com/spf13/cobra"

	"github.com/dnr/styx/ci"
)

func withAxiomLogs(c *cobra.Command) runE {
	useAxiom := c.Flags().Bool("log_axiom", false, "")

	return func(c *cobra.Command, args []string) error {
		if *useAxiom {
			h, err := axiom_slog_adapter.New()
			if err != nil {
				return err
			}
			c.PostRunE = chainRunE(c.PostRunE, func(*cobra.Command, []string) error { h.Close(); return nil })
			slog.SetDefault(slog.New(h))
		}
		return nil
	}
}

func withWorkerConfig(c *cobra.Command) runE {
	var cfg ci.WorkerConfig

	c.Flags().StringVar(&cfg.TemporalParams, "temporal_params", "", "source for temporal params")
	c.Flags().StringVar(&cfg.SmtpParams, "smtp_params", "", "source for smtp params")

	c.Flags().BoolVar(&cfg.RunWorker, "worker", false, "run temporal workflow+activity worker")
	c.Flags().BoolVar(&cfg.RunScaler, "scaler", true, "run scaler on worker")
	c.Flags().BoolVar(&cfg.RunHeavyWorker, "heavy", false, "run temporal heavy worker (on ec2)")

	c.Flags().DurationVar(&cfg.ScaleInterval, "scale_interval", time.Minute, "scaler interval")
	c.Flags().StringVar(&cfg.AsgGroupName, "scale_group_name", "", "scaler asg group name")

	c.Flags().StringVar(&cfg.CacheSignKeySSM, "cache_signkey_ssm", "", "sign nix cache with key from SSM")

	// manifest builder cfg
	c.Flags().StringArrayVar(&cfg.ManifestPubKeys, "nix_pubkey", nil, "verify narinfo with this public key")
	c.Flags().StringArrayVar(&cfg.ManifestSignKeySSM, "styx_signkey_ssm", nil, "sign manifest with key from SSM")

	// chunk store write config
	c.Flags().StringVar(&cfg.CSWCfg.ChunkBucket, "chunkbucket", "", "s3 bucket to put chunks")
	c.Flags().IntVar(&cfg.CSWCfg.ZstdEncoderLevel, "zstd_level", 9, "encoder level for zstd chunks")

	return func(c *cobra.Command, args []string) error {
		store(c, cfg)
		return nil
	}
}

func withStartConfig(c *cobra.Command) runE {
	var cfg ci.StartConfig

	c.Flags().StringVar(&cfg.TemporalParams, "temporal_params", "keys/temporal-creds-charon.secret", "source for temporal params")

	// might use these:
	c.Flags().StringVar(&cfg.Args.Channel, "nix_channel", "nixos-25.11", "nix channel to watch/build")
	c.Flags().StringVar(&cfg.Args.StyxRepo.Branch, "styx_branch", "release", "branch of styx repo to watch/build")

	// probably don't use these:
	const bucket = "styx-1"
	const subdir = "nixcache"
	const region = "us-east-1"
	const level = 9
	defCopyDest := fmt.Sprintf("s3://%s/%s/?region=%s&compression=zstd&compression-level=%d", bucket, subdir, region, level)
	// note missing region since it's us-east-1. also note trailing slash must be present to match cache key.
	defUpstream := fmt.Sprintf("https://%s.s3.amazonaws.com/%s/", bucket, subdir)
	c.Flags().StringVar(&cfg.Args.StyxRepo.Repo, "styx_repo", "https://github.com/dnr/styx/", "url of styx repo")
	c.Flags().StringVar(&cfg.Args.CopyDest, "copy_dest", defCopyDest, "store path for copying built packages")
	c.Flags().StringVar(&cfg.Args.ManifestUpstream, "manifest_upstream", defUpstream, "read-only url for dest store")
	c.Flags().StringVar(&cfg.Args.PublicCacheUpstream, "public_upstream", "https://cache.nixos.org/", "read-only url for public cache")

	return func(c *cobra.Command, args []string) error {
		store(c, cfg)
		return nil
	}
}

func withGCConfig(c *cobra.Command) runE {
	var cfg ci.GCConfig
	c.Flags().StringVar(&cfg.Bucket, "bucket", "styx-1", "s3 bucket")
	c.Flags().DurationVar(&cfg.MaxAge, "max_age", 210*24*time.Hour, "gc age")
	return func(c *cobra.Command, args []string) error {
		store(c, cfg)
		return nil
	}
}

func main() {
	root := cmd(
		&cobra.Command{
			Use:   "charon",
			Short: "charon - CI for styx",
		},
		cmd(
			&cobra.Command{Use: "worker", Short: "act as temporal worker"},
			withAxiomLogs,
			withWorkerConfig,
			func(c *cobra.Command, args []string) error {
				return ci.RunWorker(c.Context(), get[ci.WorkerConfig](c))
			},
		),
		cmd(
			&cobra.Command{Use: "start", Short: "start ci workflow"},
			withStartConfig,
			func(c *cobra.Command, args []string) error {
				return ci.Start(c.Context(), get[ci.StartConfig](c))
			},
		),
		cmd(
			&cobra.Command{Use: "gclocal", Short: "run gc from this process (mostly for testing)"},
			withGCConfig,
			func(c *cobra.Command, args []string) error {
				return ci.GCLocal(c.Context(), get[ci.GCConfig](c))
			},
		),
	)
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}
