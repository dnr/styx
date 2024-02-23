package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dnr/styx/erofs"
)

func debugNarToErofs(c *cobra.Command, args []string) error {
	narFile, outPath := args[0], args[1]
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

	return erofs.NewBuilder(in, out).Build()
}
