package main

import (
	"context"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/erofs"
	"github.com/dnr/styx/manifester"
	"github.com/dnr/styx/pb"
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

func withErofsBuiler(c *cobra.Command) runE {
	var cfg erofs.BuilderConfig
	c.Flags().IntVar(&cfg.BlockShift, "block_shift", 12, "block size bits for local fs images")
	return func(c *cobra.Command, args []string) error {
		b := erofs.NewBuilder(cfg)
		c.SetContext(context.WithValue(c.Context(), ctxErofsBuilder, b))
		return nil
	}
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
			withErofsBuiler,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				b := c.Context().Value(ctxErofsBuilder).(*erofs.Builder)
				return b.BuildFromNar(in, out)
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
			withSignKeys,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				mb := c.Context().Value(ctxManifestBuilder).(*manifester.ManifestBuilder)
				keys := c.Context().Value(ctxSignKeys).([]signature.SecretKey)
				var buildArgs manifester.BuildArgs
				if manifest, err := mb.Build(context.Background(), buildArgs, in); err != nil {
					return err
				} else if b, err := common.SignMessage(keys, manifest); err != nil {
					return err
				} else if enc, err := zstd.NewWriter(out); err != nil {
					return err
				} else if n, err := enc.Write(b); err != nil || n < len(b) {
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
			withInFile,
			withOutFile,
			withErofsBuiler,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				// FIXME
				// cs := c.Context().Value(ctxChunkStoreRead).(manifester.ChunkStoreRead)
				b := c.Context().Value(ctxErofsBuilder).(*erofs.Builder)
				return b.BuildFromManifestEmbed(c.Context(), in, out, nil)
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
			withErofsBuiler,
			withStyxPubKeys,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				b := c.Context().Value(ctxErofsBuilder).(*erofs.Builder)
				keys := c.Context().Value(ctxStyxPubKeys).([]signature.PublicKey)
				sm := erofs.NewDummySlabManager()

				// TODO: move to common place?
				var m pb.Manifest
				if zr, err := zstd.NewReader(in); err != nil {
					return err
				} else if sBytes, err := io.ReadAll(zr); err != nil {
					return err
				} else if err := common.VerifyMessage(keys, sBytes, &m); err != nil {
					return err
				}

				return b.BuildFromManifestWithSlab(&m, out, sm)
			},
		),
		cmd(
			&cobra.Command{
				Use:  "signdaemonparams <daemon params json> <out file image>",
				Args: cobra.ExactArgs(2),
			},
			withSignKeys,
			withInFile,
			withOutFile,
			func(c *cobra.Command, args []string) error {
				in := c.Context().Value(ctxInFile).(*os.File)
				out := c.Context().Value(ctxOutFile).(*os.File)
				keys := c.Context().Value(ctxSignKeys).([]signature.SecretKey)
				var params pb.DaemonParams
				var sb []byte
				if b, err := io.ReadAll(in); err != nil {
					return err
				} else if err = protojson.Unmarshal(b, &params); err != nil {
					return err
				} else if sb, err = common.SignMessage(keys, &params); err != nil {
					return err
				} else if _, err = out.Write(sb); err != nil {
					return err
				}
				return nil
			},
		),
	)
}
