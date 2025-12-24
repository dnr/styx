package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/client"
	"github.com/dnr/styx/common/cobrautil"
	"github.com/dnr/styx/daemon"
	"github.com/nix-community/go-nix/pkg/storepath"
	"github.com/spf13/cobra"
)

type tarballArgs struct {
	realise   bool
	printJson bool
	shards    int
}

var tarballCmd = cobrautil.Cmd(
	&cobra.Command{
		Use:   "tarball <url>",
		Short: "'substitute' a generic tarball to the local store",
		Long: `With no flags, realises the tarball with Styx and prints the store path on stdout.
With --json, does not realise and prints json metadata that can be used to build a derivation.
With --json --realise, realises and then prints json.`,
		Args: cobra.ExactArgs(1),
	},
	withStyxClient,
	func(c *cobra.Command) *tarballArgs {
		var args tarballArgs
		c.Flags().BoolVarP(&args.printJson, "json", "j", false, "print json info for fixed-output derivation")
		c.Flags().BoolVar(&args.realise, "realise", false, "realise tarball (always without --json)")
		c.Flags().IntVar(&args.shards, "shards", 0, "split up manifesting")
		return &args
	},
	func(c *cobra.Command, args []string, targs *tarballArgs) error {
		cli := cobrautil.GetKeyed[*client.StyxClient](c, "public")

		// ask daemon to ask manifester to ingest this tarball
		var res daemon.TarballResp
		status, err := cli.Call(daemon.TarballPath, &daemon.TarballReq{
			UpstreamUrl: args[0],
			Shards:      targs.shards,
		}, &res)
		if err != nil {
			return err
		} else if status != http.StatusOK {
			return common.NewHttpError(status, "")
		}

		if storepath.NameRe.FindString(res.StorePathName) != res.StorePathName {
			return fmt.Errorf("invalid name %q", res.StorePathName)
		}

		// realise it
		if targs.realise || !targs.printJson {
			sp := "/nix/store/" + res.StorePathHash + "-" + res.StorePathName
			cmd := exec.CommandContext(c.Context(), "nix-store", "--realise", sp)
			if targs.printJson {
				cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			} else {
				cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			}
			if err := cmd.Run(); err != nil {
				return err
			}
		}

		if targs.printJson {
			return json.NewEncoder(os.Stdout).Encode(res)
		}
		return nil
	},
)
