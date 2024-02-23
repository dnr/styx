package main

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/dnr/styx/daemon"
	"github.com/dnr/styx/manifester"
)

func main() {
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "act as local daemon",
	}
	var dcfg daemon.Config
	daemonCmd.Flags().StringVar(&dcfg.DevPath, "devpath", "/dev/cachefiles", "path to cachefiles device node")
	daemonCmd.Flags().StringVar(&dcfg.CachePath, "cache", "/var/cache/styx",
		"path to local cache (also socket and db)")
	daemonCmd.RunE = func(c *cobra.Command, args []string) error {
		return daemon.CachefilesServer(dcfg).Run()
	}

	manifesterCmd := &cobra.Command{
		Use:   "manifester",
		Short: "act as manifester server",
	}
	var mcfg manifester.Config
	manifesterCmd.Flags().StringVar(&mcfg.Bind, "bind", ":7420", "address to listen on")
	manifesterCmd.Flags().StringVar(&mcfg.Upstream, "upstream", "cache.nixos.org", "upstream binary cache")
	manifesterCmd.Flags().StringVar(&mcfg.ChunkBucket, "chunkbucket", "",
		"s3 bucket to put chunks and cached manifests")
	manifesterCmd.Flags().StringVar(&mcfg.ChunkLocalDir, "chunklocaldir", "", "local directory to put chunks")
	manifesterCmd.RunE = func(c *cobra.Command, args []string) error {
		return manifester.ManifestServer(mcfg).Run()
	}

	clientCmd := &cobra.Command{
		Use:     "client",
		Aliases: []string{"c"},
		Short:   "client to local daemon",
	}
	clientCmd.AddCommand(
		&cobra.Command{
			Use:   "mount <nar file or store path> <mount point>",
			Short: "mounts a nix package",
			Args:  cobra.ExactArgs(2),
			RunE:  func(c *cobra.Command, args []string) error { return nil },
		},
	)

	debugCmd := &cobra.Command{Use: "debug", Aliases: []string{"d"}, Short: "debug commands"}
	debugCmd.AddCommand(
		&cobra.Command{
			Use:   "nartoerofs <nar file> <erofs image>",
			Short: "create an erofs image from a nar file",
			Args:  cobra.ExactArgs(2),
			RunE:  debugNarToErofs,
		},
	)

	root := &cobra.Command{
		Use:   "styx",
		Short: "styx - streaming filesystem for nix",
	}
	root.AddCommand(
		daemonCmd, manifesterCmd, clientCmd, debugCmd,
	)
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}
