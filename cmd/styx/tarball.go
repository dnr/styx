package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/dnr/styx/common"
	"github.com/dnr/styx/common/client"
	"github.com/dnr/styx/common/cobrautil"
	"github.com/dnr/styx/daemon"
	"github.com/nix-community/go-nix/pkg/storepath"
	"github.com/spf13/cobra"
)

type tarballArgs struct {
	outlink   string
	printDrv  bool
	printJson bool
	shards    int
}

var tarballCmd = cobrautil.Cmd(
	&cobra.Command{
		Use:   "tarball <url>",
		Short: "'substitute' a generic tarball to the local store",
		Args:  cobra.ExactArgs(1),
	},
	withStyxClient,
	func(c *cobra.Command) *tarballArgs {
		var args tarballArgs
		c.Flags().StringVarP(&args.outlink, "out-link", "o", "", "symlink this to output and register as nix root")
		c.Flags().BoolVarP(&args.printDrv, "print", "p", false, "print nix code for fixed-output derivation")
		c.Flags().BoolVarP(&args.printJson, "json", "j", false, "print json info for fixed-output derivation")
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

		if targs.printJson {
			return json.NewEncoder(os.Stdout).Encode(res)
		}

		// we want to tell nix to just "substitute this", but it needs a derivation, so
		// construct a minimal derivation. this is not buildable, it just has the right
		// store path and is a FOD. the styx daemon acts as a "binary cache" for this store
		// path, so nix will ask styx to do the substitution.
		derivationStr := fmt.Sprintf(`derivation {
  name = %s;
  system = builtins.currentSystem;
  builder = "not buildable: run 'styx tarball <url>' and try again";
  outputHash = "%s:%s";
  outputHashMode = "recursive";

  styxOriginalUrl = %s;
  styxResolvedUrl = %s;
}`, escapeNixString(res.StorePathName), res.NarHashAlgo, res.NarHash, escapeNixString(args[0]), escapeNixString(res.ResolvedUrl))

		if targs.printDrv {
			// TODO: print something using builtins.fetchTarball instead so it can be
			// evaluated without styx but also substitute.
			// needs to wait for: https://github.com/NixOS/nix/pull/14138 in 2.32.0
			fmt.Println(derivationStr)
			return nil
		}

		// realize the derivation
		cmd := exec.CommandContext(c.Context(), "nix-build", "-E", derivationStr, "--no-fallback")
		if targs.outlink != "" {
			cmd.Args = append(cmd.Args, "--out-link", targs.outlink)
		} else {
			cmd.Args = append(cmd.Args, "--no-out-link")
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	},
)

func escapeNixString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := range len(s) {
		switch c := s[i]; c {
		case '"', '\\', '$':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
