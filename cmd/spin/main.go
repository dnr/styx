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

type transform func(context.Context, []string, *pinJson) error

func writePinsNix() error {
	if have, err := os.ReadFile(pinsNixName); err == nil && bytes.Equal(have, pinsNixCode) {
		return nil
	}
	return os.WriteFile(pinsNixName, pinsNixCode, 0o644)
}

func loadOrCreatePinJson() (*pinJson, error) {
	b, err := os.ReadFile(pinsJsonName)
	var j pinJson
	if err == nil {
		err := json.Unmarshal(b, &j)
		if err != nil {
			return nil, err
		} else if j.StoreDir != storepath.StoreDir {
			return nil, fmt.Errorf("mismatched store dir %q != %q", j.StoreDir, storepath.StoreDir)
		} else if j.Version != spinVersion {
			return nil, fmt.Errorf("mismatched version %q != %q", j.Version, spinVersion)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	j.Doc = doc
	j.Schema = schemaUrl
	j.StoreDir = storepath.StoreDir
	j.Version = spinVersion
	return &j, nil
}

func savePinJson(j *pinJson) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pinsJsonName, b, 0o644)
}

func transformAction(f transform) any {
	return func(ctx context.Context, args []string) error {
		if j, err := loadOrCreatePinJson(); err != nil {
			return err
		} else if err := f(ctx, args, j); err != nil {
			return err
		} else {
			return savePinJson(j)
		}
	}
}

func (j *pinJson) findPin(name string) *pinData {
	for _, d := range j.Pins {
		if d.Name == name {
			return d
		}
	}
	return nil
}

func (d *pinData) update() error {
	// copied from daemon/proto.go TarballResp to avoid dependency
	var out struct {
		ResolvedUrl   string `json:"resolvedUrl"`
		StorePathHash string `json:"storePathHash"`
		StorePathName string `json:"storePathName"`
		NarHash       string `json:"narHash"`
		NarHashAlgo   string `json:"narHashAlgo"`
	}
	log.Println("running: styx tarball --json", d.OriginalUrl)
	b, err := exec.Command("styx", "tarball", "--json", d.OriginalUrl).Output()
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
			transformAction(func(ctx context.Context, args []string, j *pinJson) error {
				return nil
			}),
			writePinsNix,
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "add <name> <url>",
				Short: "add new pin",
				Args:  cobra.ExactArgs(2),
			},
			transformAction(func(ctx context.Context, args []string, j *pinJson) error {
				name, url := args[0], args[1]
				if j.findPin(name) != nil {
					return fmt.Errorf("Pin %q already exists")
				}
				d := &pinData{
					Name:        name,
					OriginalUrl: url,
				}
				j.Pins = append(j.Pins, d)
				return d.update()
			}),
			writePinsNix,
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "remove <name>",
				Short: "remove pin",
				Args:  cobra.ExactArgs(1),
			},
			transformAction(func(ctx context.Context, args []string, j *pinJson) error {
				d := j.findPin(args[0])
				if d == nil {
					return fmt.Errorf("Pin %q not found", args[0])
				}
				j.Pins = slices.DeleteFunc(j.Pins, func(pd *pinData) bool { return pd == d })
				return nil
			}),
			writePinsNix,
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "update <name>",
				Short: "update one pin",
				Args:  cobra.ExactArgs(1),
			},
			transformAction(func(ctx context.Context, args []string, j *pinJson) error {
				d := j.findPin(args[0])
				if d == nil {
					return fmt.Errorf("Pin %q not found", args[0])
				}
				return d.update()
			}),
			writePinsNix,
		),
		cobrautil.Cmd(
			&cobra.Command{
				Use:   "updateall",
				Short: "update all pins",
				Args:  cobra.NoArgs,
			},
			transformAction(func(ctx context.Context, args []string, j *pinJson) error {
				for _, d := range j.Pins {
					if err := d.update(); err != nil {
						return err
					}
				}
				return nil
			}),
			writePinsNix,
		),
	)
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}
