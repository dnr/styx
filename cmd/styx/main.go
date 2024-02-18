package main

import (
	"log"
	"os"

	"github.com/spf13/cobra"

	"github.com/dnr/styx"
)

func main() {
	root := &cobra.Command{
		Use:   "styx",
		Short: "styx - streaming filesystem for nix",
	}
	root.AddCommand(&cobra.Command{
		Use:   "nartoerofs <nar file> <erofs image>",
		Short: "create an erofs image from a nar file",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return narToErofs(args[0], args[1])
		},
	}, &cobra.Command{
		Use:   "cachefilesd",
		Short: "act as cachefiles daemon",
		RunE: func(c *cobra.Command, args []string) error {
			return styx.CachefilesServer().Run()
		},
	})
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}

}

func narToErofs(narFile, outPath string) error {
	// in, cleanup, err := styx.GetNarFromNixDump(pathOrHash)
	// if err != nil {
	// 	return err
	// }
	// defer cleanup()

	in, err := os.Open(narFile)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}

	return styx.NewBuilder(in, out).Build()
}
