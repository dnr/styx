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

	c.Flags().StringVar(&cfg.HostPort, "temporal_hostport", "", "temporal hostport")
	c.Flags().StringVar(&cfg.Namespace, "temporal_namespace", "charon", "temporal namespace")
	c.Flags().StringVar(&cfg.ApiKey, "temporal_apikey", "", "temporal api key")

	c.Flags().BoolVar(&cfg.RunWorker, "worker", false, "run temporal workflow+activity worker")
	c.Flags().BoolVar(&cfg.RunScaler, "scaler", true, "run scaler on worker")
	c.Flags().BoolVar(&cfg.RunHeavyWorker, "heavyworker", false, "run temporal heavy worker (on ec2)")

	c.Flags().DurationVar(&cfg.ScaleInterval, "scale_interval", time.Minute, "scaler interval")
	c.Flags().StringVar(&cfg.AsgGroupName, "scale_group_name", "styx-charon-asg", "scaler asg group name")

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
