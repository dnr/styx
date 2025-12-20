package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/dnr/styx/common/client"
	"github.com/dnr/styx/daemon"
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
		cli := get[*client.StyxClient](c)
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

		// if running in sudo, try to run commands as original user
		var spa syscall.SysProcAttr
		uid, erru := strconv.Atoi(os.Getenv("SUDO_UID"))
		gid, errg := strconv.Atoi(os.Getenv("SUDO_GID"))
		if erru == nil && errg == nil {
			spa.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
		}

		if strings.ContainsFunc(resp.Name, func(r rune) bool {
			return r < 32 || r > 126 || r == '$' || r == '/' || r == '\\' || r == '"'
		}) {
			return fmt.Errorf("invalid name %q", resp.Name)
		}

		// we want to tell nix to just "substitute this", but it needs a derivation, so
		// construct a minimal derivation. this is not buildable, it just has the right
		// store path and is a FOD. the styx daemon acts as a "binary cache" for this store
		// path, so nix will ask styx to do the substitution.
		derivationStr := fmt.Sprintf(`derivation {
  name = "%s";
  system = builtins.currentSystem;
  builder = "/bin/false";
  outputHash = "%s:%s";
  outputHashMode = "recursive";
}`, resp.Name, resp.NarHashAlgo, resp.NarHash)

		if targs.printOnly {
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
		cmd.SysProcAttr = &spa
		return cmd.Run()
	},
)
