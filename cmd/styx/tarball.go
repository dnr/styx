package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/dnr/styx/common/client"
	"github.com/dnr/styx/daemon"
	"github.com/nix-community/go-nix/pkg/storepath"
	"github.com/spf13/cobra"
)

type tarballArgs struct {
	outlink   string
	printOnly bool
	shards    int
}

var tarballCmd = cmd(
	&cobra.Command{
		Use:   "tarball <url>",
		Short: "'substitute' a generic tarball to the local store",
		Args:  cobra.ExactArgs(1),
	},
	withStyxClient,
	func(c *cobra.Command) runE {
		var args tarballArgs
		c.Flags().StringVarP(&args.outlink, "out-link", "o", "", "symlink this to output and register as nix root")
		c.Flags().BoolVarP(&args.printOnly, "print", "p", false, "print nix code for fixed-output derivation")
		c.Flags().IntVar(&args.shards, "shards", 0, "split up manifesting")
		return storer(&args)
	},
	func(c *cobra.Command, args []string) error {
		cli := getKeyed[*client.StyxClient](c, "public")
		targs := get[*tarballArgs](c)

		// ask daemon to ask manifester to ingest this tarball
		var resp daemon.TarballResp
		status, err := cli.Call(daemon.TarballPath, &daemon.TarballReq{
			UpstreamUrl: args[0],
			Shards:      targs.shards,
		}, &resp)
		if err != nil {
			fmt.Println("call error:", err)
			return err
		} else if status != http.StatusOK {
			fmt.Println("status:", status)
		}

		if storepath.NameRe.FindString(resp.Name) != resp.Name {
			return fmt.Errorf("invalid name %q", resp.Name)
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
}`,
			escapeNixString(resp.Name), resp.NarHashAlgo, resp.NarHash, escapeNixString(args[0]), escapeNixString(resp.ResolvedUrl))

		if targs.printOnly {
			// TODO: this should print something using builtins.fetchTarball instead so it can
			// be evaluated without styx but also substitute
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
