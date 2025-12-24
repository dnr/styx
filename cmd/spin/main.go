package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"

	"github.com/dnr/styx/common/cobrautil"
	"github.com/nix-community/go-nix/pkg/storepath"
	"github.com/spf13/cobra"
)

const (
	spinVersion  = "spin-1"
	doc          = "This file contains Nix pins using the 'spin' styx-based pinning tool. See https://github.com/dnr/styx/tree/main/cmd/spin"
	schemaUrl    = "TODO"
	pinsJsonName = "Pins.json"
	pinsNixName  = "Pins.nix"
)

//go:embed Pins.nix
var pinsNixCode []byte

type pinJson struct {
	Doc      string     `json:"$doc"`
	Schema   string     `json:"$schema"`
	StoreDir string     `json:"$storeDir"`
	Version  string     `json:"$version"`
	Pins     []*pinData `json:"pins"`
}

type pinData struct {
	Name          string `json:"name"`
	StorePathName string `json:"storePathName"`
	OutputHash    string `json:"outputHash"`
	OriginalUrl   string `json:"originalUrl"`
	ResolvedUrl   string `json:"resolvedUrl"`
}

func updatePinsNix() error {
	if have, err := os.ReadFile(pinsNixName); err == nil && bytes.Equal(have, pinsNixCode) {
		return nil
	}
	return os.WriteFile(pinsNixName, pinsNixCode, 0o644)
}

func loadOrCreatePinJson(c *cobra.Command) error {
	b, err := os.ReadFile(pinsJsonName)
	var j pinJson
	if err == nil {
		err := json.Unmarshal(b, &j)
		if err != nil {
			return err
		} else if j.StoreDir != storepath.StoreDir {
			return fmt.Errorf("mismatched store dir %q != %q", j.StoreDir, storepath.StoreDir)
		} else if j.Version != spinVersion {
			return fmt.Errorf("mismatched version %q != %q", j.Version, spinVersion)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	j.Doc = doc
	j.Schema = schemaUrl
	j.StoreDir = storepath.StoreDir
	j.Version = spinVersion
	cobrautil.Store(c, &j)
	return nil
}

func savePinJson(j *pinJson) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pinsJsonName, b, 0o644)
}

func (d *pinData) update(ctx context.Context) error {
	// copied from daemon/proto.go TarballResp to avoid dependency
	var out struct {
		ResolvedUrl   string `json:"resolvedUrl"`
		StorePathHash string `json:"storePathHash"`
		StorePathName string `json:"storePathName"`
		NarHash       string `json:"narHash"`
		NarHashAlgo   string `json:"narHashAlgo"`
	}
	log.Println("running: styx tarball --json", d.OriginalUrl)
	b, err := exec.CommandContext(ctx, "styx", "tarball", "--json", d.OriginalUrl).Output()
	if err != nil {
		return err
	} else if err = json.Unmarshal(b, &out); err != nil {
		return err
	}
	log.Println("resolved to", out.ResolvedUrl)
	log.Println("using name", out.StorePathName)
	d.ResolvedUrl = out.ResolvedUrl
	d.StorePathName = out.StorePathName
	d.OutputHash = out.NarHashAlgo + ":" + out.NarHash
	return nil
}

func (d *pinData) refresh(ctx context.Context) error {
	log.Println("running: styx tarball --json", d.ResolvedUrl)
	_, err := exec.CommandContext(ctx, "styx", "tarball", "--json", d.ResolvedUrl).Output()
	return err
}

func withAllFlag(c *cobra.Command) *bool {
	return c.Flags().Bool("all", false, "apply to all pins")
}

func main() {
	root := cobrautil.Cmd(
		&cobra.Command{
			Use:   "spin",
			Short: "Spin - simple Nix pinning tool using Styx",
		},
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "init",
				Short: "init or re-init spin",
				Args:  cobra.NoArgs,
			},
			loadOrCreatePinJson,
			savePinJson,
			updatePinsNix,
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "refresh [--all] <name> ...",
				Short: "tell Styx about pins again so that they can be substituted",
			},
			withAllFlag,
			loadOrCreatePinJson,
			func(ctx context.Context, args []string, all *bool, j *pinJson) error {
				for _, d := range j.Pins {
					if *all || slices.Contains(args, d.Name) {
						if err := d.refresh(ctx); err != nil {
							return err
						}
					}
				}
				return nil
			},
			savePinJson,
			updatePinsNix,
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "add <name> <url>",
				Short: "add new pin",
				Args:  cobra.ExactArgs(2),
			},
			loadOrCreatePinJson,
			func(ctx context.Context, args []string, j *pinJson) error {
				name, url := args[0], args[1]
				for _, d := range j.Pins {
					if d.Name == name {
						return fmt.Errorf("Pin %q already exists", name)
					}
				}
				d := &pinData{
					Name:        name,
					OriginalUrl: url,
				}
				j.Pins = append(j.Pins, d)
				return d.update(ctx)
			},
			savePinJson,
			updatePinsNix,
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "update [--all] <name> ...",
				Short: "update pin(s)",
			},
			withAllFlag,
			loadOrCreatePinJson,
			func(ctx context.Context, args []string, all *bool, j *pinJson) error {
				for _, d := range j.Pins {
					if *all || slices.Contains(args, d.Name) {
						if err := d.update(ctx); err != nil {
							return err
						}
					}
				}
				return nil
			},
			savePinJson,
			updatePinsNix,
		),
	)
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}
