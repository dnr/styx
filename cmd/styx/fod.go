package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"

	"github.com/dnr/styx/common/client"
	"github.com/dnr/styx/daemon"
	"github.com/nix-community/go-nix/pkg/storepath"
	"github.com/spf13/cobra"
)

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

type drvOutput struct {
	Hash     string `json:"hash"`
	HashAlgo string `json:"hashAlgo"`
	Method   string `json:"method"`
	Path     string `json:"path"`
}

var fodCmd = cmd(
	&cobra.Command{
		Use:   "fod <url>",
		Short: "'substitute' a generic tarball to the local store",
		Args:  cobra.ExactArgs(1),
	},
	withStyxClient,
	func(c *cobra.Command, args []string) error {
		cli := get[*client.StyxClient](c)
		var resp daemon.GenericFodResp
		status, err := cli.Call(daemon.GenericFodPath, &daemon.GenericFodReq{
			UpstreamUrl: args[0],
		}, &resp)
		if err != nil {
			fmt.Println("call error:", err)
			return err
		} else if status != http.StatusOK {
			fmt.Println("status:", status)
		}

		path := storepath.StoreDir + "/" + resp.StorePathHash + "-" + resp.Name
		drvBytes, err := json.Marshal(drvJson{
			Name:    resp.Name,
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
					Hash:     resp.NarHash,
					HashAlgo: resp.NarHashAlgo,
					Method:   "nar",
					Path:     path,
				},
			},
		})
		if err != nil {
			return err
		}

		cmd := exec.Command("nix", "--extra-experimental-features", "nix-command", "derivation", "add")
		cmd.Stdin = bytes.NewReader(drvBytes)
		drvPath, err := cmd.Output()
		if err != nil {
			return err
		}

		// TODO: sign it so we don't need --no-requre-sigs
		log.Println("run this to realize the tarball:")
		log.Println("sudo", "nix-store", "--no-require-sigs", "--add-root", resp.Name, "-r", string(drvPath))
		return nil
	},
)
