package main

import (
	"io"
	"os"

	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/pb"
)

func withInFile(c *cobra.Command, args []string) error {
	in, err := os.Open(args[0])
	if err != nil {
		return err
	}
	storeKeyed(c, in, "in")
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
	storeKeyed(c, out, "out")
	c.PostRunE = chainRunE(c.PostRunE, func(c *cobra.Command, args []string) error {
		return out.Close()
	})
	return nil
}

func internalCmd() *cobra.Command {
	return cmd(
		&cobra.Command{Use: "internal", Short: "internal commands"},
		cmd(
			&cobra.Command{
				Use:  "signdaemonparams <daemon params json> <out file image>",
				Args: cobra.ExactArgs(2),
			},
			withSignKeys,
			withInFile,
			withOutFile,
			func(c *cobra.Command, args []string) error {
				in := getKeyed[*os.File](c, "in")
				out := getKeyed[*os.File](c, "out")
				keys := get[[]signature.SecretKey](c)
				var params pb.DaemonParams
				var sb []byte
				if b, err := io.ReadAll(in); err != nil {
					return err
				} else if err = protojson.Unmarshal(b, &params); err != nil {
					return err
				} else if sb, err = common.SignInlineMessage(keys, common.DaemonParamsContext, &params); err != nil {
					return err
				} else if _, err = out.Write(sb); err != nil {
					return err
				}
				return nil
			},
		),
	)
}
