package main

import (
	"context"
	"os"

	"github.com/klauspost/compress/zstd"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
)

func debugNarToErofs(c *cobra.Command, args []string) error {
	narFile, outPath := args[0], args[1]
	if in, err := os.Open(narFile); err != nil {
		return err
	} else if out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err != nil {
		return err
	} else {
		return erofs.NewBuilder().BuildFromNar(in, out)
	}
}

func debugNarToManifest(c *cobra.Command, args []string, cscfg manifester.ChunkStoreConfig) error {
	narFile, outPath := args[0], args[1]
	var mbcfg manifester.ManifestBuilderConfig
	if in, err := os.Open(narFile); err != nil {
		return err
	} else if cs, err := manifester.NewChunkStore(cscfg); err != nil {
		return err
	} else if b, err := manifester.NewManifestBuilder(mbcfg, cs); err != nil {
		return err
	} else if manifest, err := b.Build(context.Background(), in); err != nil {
		return err
	} else if mbytes, err := proto.Marshal(manifest); err != nil {
		return err
	} else if enc, err := zstd.NewWriter(nil); err != nil {
		return err
	} else {
		cbytes := enc.EncodeAll(mbytes, nil)
		return os.WriteFile(outPath, cbytes, 0644)
	}
}
