//go:build linux

// Copyright 2018 Axel Wagner
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	"github.com/Merovius/nbd"
	"github.com/google/subcommands"
)

func init() {
	commands = append(commands, &serveCmd{})
}

type serveCmd struct {
	addr string
	unix bool
}

func (cmd *serveCmd) Name() string {
	return "serve"
}

func (cmd *serveCmd) Synopsis() string {
	return "serve a file as a block device"
}

func (cmd *serveCmd) Usage() string {
	return `Usage: nbd serve <file>

Serve a file as over NBD as a block device.
`
}

func (cmd *serveCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&cmd.addr, "addr", "localhost:10809", "Address to listen on")
	fs.BoolVar(&cmd.unix, "unix", false, "Serve on a unix domain socket")
}

func (cmd *serveCmd) Execute(ctx context.Context, fs *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	if fs.NArg() != 1 {
		log.Print(cmd.Usage())
		return subcommands.ExitUsageError
	}

	f, err := os.OpenFile(fs.Arg(0), os.O_RDWR, 0)
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	network := "tcp"
	if cmd.unix {
		network = "unix"
	}

	err = nbd.ListenAndServe(ctx, network, cmd.addr, nbd.Export{
		Name:        filepath.Base(fs.Arg(0)),
		Description: "",
		Size:        uint64(fi.Size()),
		BlockSizes:  blockSize(fi),
		Device:      f,
	})
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	return subcommands.ExitSuccess
}
