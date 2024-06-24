package main

import (
	"context"
	"log"
	"time"

	"github.com/spf13/cobra"

	"github.com/dnr/styx/ci"
)

const (
	ctxWorkerConfig = iota
)

func withWorkerConfig(c *cobra.Command) runE {
	var cfg ci.WorkerConfig

	c.Flags().StringVar(&cfg.TemporalSSM, "temporal_ssm", "", "get temporal params from ssm")
	c.Flags().StringVar(&cfg.HostPort, "temporal_hostport", "", "temporal hostport")
	c.Flags().StringVar(&cfg.Namespace, "temporal_namespace", "", "temporal namespace")
	c.Flags().StringVar(&cfg.ApiKey, "temporal_apikey", "", "temporal api key")

	c.Flags().BoolVar(&cfg.RunWorker, "worker", false, "run temporal workflow+activity worker")
	c.Flags().BoolVar(&cfg.RunScaler, "scaler", true, "run scaler on worker")
	c.Flags().BoolVar(&cfg.RunHeavyWorker, "heavy", false, "run temporal heavy worker (on ec2)")

	c.Flags().DurationVar(&cfg.ScaleInterval, "scale_interval", time.Minute, "scaler interval")
	c.Flags().StringVar(&cfg.AsgGroupName, "scale_group_name", "styx-charon-asg", "scaler asg group name")

	// manifest builder cfg
	c.Flags().IntVar(&cfg.MBCfg.ChunkShift, "chunk_shift", 16, "chunk shift for building manifests")
	c.Flags().StringVar(&cfg.MBCfg.DigestAlgo, "digest_algo", "sha256", "digest algo for building manifests")
	c.Flags().IntVar(&cfg.MBCfg.DigestBits, "digest_bits", 192, "digest bits for building manifests")
	c.Flags().StringArrayVar(&cfg.ManifestPubKeys, "nix_pubkey",
		[]string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="},
		"verify narinfo with this public key")
	c.Flags().StringArrayVar(&cfg.ManifestSignKeySSM, "styx_signkey_ssm", nil, "sign manifest with key from SSM")

	// chunk store write config
	c.Flags().StringVar(&cfg.CSWCfg.ChunkBucket, "chunkbucket", "", "s3 bucket to put chunks")
	c.Flags().IntVar(&cfg.CSWCfg.ZstdEncoderLevel, "zstd_level", 9, "encoder level for zstd chunks")

	return func(c *cobra.Command, args []string) error {
		c.SetContext(context.WithValue(c.Context(), ctxWorkerConfig, cfg))
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
			withWorkerConfig,
			func(c *cobra.Command, args []string) error {
				cfg := c.Context().Value(ctxWorkerConfig).(ci.WorkerConfig)
				return ci.RunWorker(c.Context(), cfg)
			},
		),
	)
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}
