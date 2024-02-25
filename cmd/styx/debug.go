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

func withInFile(c *cobra.Command, args []string) error {
	in, err := os.Open(args[0])
	if err != nil {
		return err
	}
	c.SetContext(context.WithValue(c.Context(), ctxInFile, in))
	c.PostRunE = chainRunE(c.PostRunE, func(c *cobra.Command, args []string) error {
		return in.Close()
	})
	return nil
}

func withOutFile(c *cobra.Command, args []string) error {
	out, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	c.SetContext(context.WithValue(c.Context(), ctxOutFile, out))
	c.PostRunE = chainRunE(c.PostRunE, func(c *cobra.Command, args []string) error {
		return out.Close()
	})
	return nil
}

func debugCmd() *cobra.Command {
	return cmd(
		&cobra.Command{Use: "debug", Aliases: []string{"d"}, Short: "debug commands"},
		cmd(
			&cobra.Command{
				Use:   "nartoerofs <nar file> <erofs image>",
				Short: "create an erofs image from a nar file",
				Args:  cobra.ExactArgs(2),
			},
			withInFile,
			withOutFile,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				return erofs.NewBuilder().BuildFromNar(in, out)
			},
		),
		cmd(
			&cobra.Command{
				Use:   "nartomanifest <nar file> <manifest>",
				Short: "create a manifest from a nar file",
				Args:  cobra.ExactArgs(2),
			},
			withManifestBuilder,
			withInFile,
			withOutFile,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				mb := c.Context().Value(ctxManifestBuilder).(*manifester.ManifestBuilder)
				if manifest, err := mb.Build(context.Background(), in); err != nil {
					return err
				} else if mbytes, err := proto.Marshal(manifest); err != nil {
					return err
				} else if enc, err := zstd.NewWriter(out); err != nil {
					return err
				} else if n, err := enc.Write(mbytes); err != nil || n < len(mbytes) {
					return err
				} else {
					return enc.Close()
				}
			},
		),
		cmd(
			&cobra.Command{
				Use:   "manifesttoerofs <manifest> <erofs image>",
				Short: "create an erofs image from a manifest",
				Args:  cobra.ExactArgs(2),
			},
			withChunkStoreRead,
			withInFile,
			withOutFile,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				cs := c.Context().Value(ctxChunkStoreRead).(manifester.ChunkStoreRead)
				return erofs.NewBuilder().BuildFromManifestEmbed(c.Context(), in, out, cs)
			},
		),
		cmd(
			&cobra.Command{
				Use:   "manifesttoerofsslab <manifest> <erofs image>",
				Short: "create an erofs image from a manifest",
				Args:  cobra.ExactArgs(2),
			},
			withInFile,
			withOutFile,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				sm := erofs.NewDummySlabManager()
				return erofs.NewBuilder().BuildFromManifestWithSlab(c.Context(), in, out, sm)
			},
		),
	)
}
