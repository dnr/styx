package main

import (
	"bytes"
	"encoding/json"
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
	outlink string
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
		return storer(&args)
	},
	func(c *cobra.Command, args []string) error {
		cli := get[*client.StyxClient](c)
		targs := get[*tarballArgs](c)

		// ask daemon to ask manifester to ingest this tarball
		var resp daemon.TarballResp
		status, err := cli.Call(daemon.TarballPath, &daemon.TarballReq{
			UpstreamUrl: args[0],
		}, &resp)
		if err != nil {
			fmt.Println("call error:", err)
			return err
		} else if status != http.StatusOK {
			fmt.Println("status:", status)
		}

		// create and add derivation for the store path
		drvPath, err := instantiateFod(resp.Name, resp.StorePathHash, resp.NarHash, resp.NarHashAlgo)
		if err != nil {
			return err
		}

		// realize the derivation
		cmd := exec.Command("nix-store", "--realise", drvPath)
		if targs.outlink != "" {
			cmd.Args = append(cmd.Args, "--add-root", targs.outlink)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	},
)

func instantiateFod(name, sphStr, narHash, narHashAlgo string) (string, error) {
	// we want to tell nix to just "substitute this", but it needs a derivation, so
	// construct a minimal derivation. this is not buildable, it just has the right
	// store path and is a FOD. the styx daemon acts as a "binary cache" for this store
	// path, so nix will ask styx to do the substitution.
	b, err := makeFodJson(name, sphStr, narHash, narHashAlgo)
	if err != nil {
		return "", err
	}

	cmd := exec.Command("nix", "--extra-experimental-features", "nix-command", "derivation", "add")
	cmd.Stdin = bytes.NewReader(b)
	drvPath, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(drvPath)), nil
}

func makeFodJson(name, sphStr, narHash, narHashAlgo string) ([]byte, error) {
	type drvOutput struct {
		Hash     string `json:"hash"`
		HashAlgo string `json:"hashAlgo"`
		Method   string `json:"method"`
		Path     string `json:"path"`
	}
	type drvJson struct {
		Name      string               `json:"name"`
		System    string               `json:"system"`
		Builder   string               `json:"builder"`
		Args      []string             `json:"args"`
		Env       map[string]string    `json:"env"`
		InputSrcs []any                `json:"inputSrcs"`
		InputDrvs map[string]any       `json:"inputDrvs"`
		Outputs   map[string]drvOutput `json:"outputs"`
	}

	path := storepath.StoreDir + "/" + sphStr + "-" + name
	return json.Marshal(drvJson{
		Name:    name,
		System:  "x86_64-linux", // TODO: does this matter?
		Builder: "/bin/false",
		Args:    []string{},
		Env: map[string]string{
			"out": path,
		},
		InputSrcs: []any{},
		InputDrvs: map[string]any{},
		Outputs: map[string]drvOutput{
			"out": drvOutput{
				Hash:     narHash,
				HashAlgo: narHashAlgo,
				Method:   "nar",
				Path:     path,
			},
		},
	})
}
