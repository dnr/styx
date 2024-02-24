package main

import (
	"log"
	"os"

	"github.com/nix-community/go-nix/pkg/narinfo/signature"
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

	var cscfg manifester.ChunkStoreConfig
	csflags := func(c *cobra.Command) {
		c.Flags().StringVar(&cscfg.ChunkBucket, "chunkbucket", "", "s3 bucket to put chunks and cached manifests")
		c.Flags().StringVar(&cscfg.ChunkLocalDir, "chunklocaldir", "", "local directory to put chunks")
	}

	manifesterCmd := &cobra.Command{
		Use:   "manifester",
		Short: "act as manifester server",
	}
	var mcfg manifester.Config
	manifesterCmd.Flags().StringVar(&mcfg.Bind, "bind", ":7420", "address to listen on")
	manifesterCmd.Flags().StringArrayVar(&mcfg.AllowedUpstreams, "allowed-upstream", []string{"cache.nixos.org"}, "allowed upstream binary caches")
	csflags(manifesterCmd)
	pubkeys := manifesterCmd.Flags().StringArray("pubkey",
		[]string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="},
		"verify narinfo with this public key")
	signkeys := manifesterCmd.Flags().StringArray("signkey", nil, "sign manifest with key from this file")

	manifesterCmd.RunE = func(c *cobra.Command, args []string) error {
		mcfg.ChunkStore = cscfg
		for _, pk := range *pubkeys {
			if k, err := signature.ParsePublicKey(pk); err != nil {
				return err
			} else {
				mcfg.PublicKeys = append(mcfg.PublicKeys, k)
			}
		}
		for _, path := range *signkeys {
			if skdata, err := os.ReadFile(path); err != nil {
				return err
			} else if k, err := signature.LoadSecretKey(string(skdata)); err != nil {
				return err
			} else {
				mcfg.SigningKeys = append(mcfg.SigningKeys, k)
			}
		}
		m, err := manifester.ManifestServer(mcfg)
		if err != nil {
			return err
		}
		return m.Run()
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
	nartomanifestCmd := &cobra.Command{
		Use:   "nartomanifest <nar file> <manifest>",
		Short: "create a manifest from a nar file",
		Args:  cobra.ExactArgs(2),
		RunE:  func(c *cobra.Command, args []string) error { return debugNarToManifest(c, args, cscfg) },
	}
	csflags(nartomanifestCmd)
	debugCmd.AddCommand(nartomanifestCmd)

	// debugCmd.AddCommand(
	// 	&cobra.Command{
	// 		Use:   "manifesttoerofs <manifest> <erofs image>",
	// 		Short: "create a manifest from a nar file",
	// 		Args:  cobra.ExactArgs(2),
	// 		RunE:  debugManifestErofs,
	// 	},
	// )

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
