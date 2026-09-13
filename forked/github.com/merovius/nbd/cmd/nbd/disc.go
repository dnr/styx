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

	"github.com/Merovius/nbd/nbdnl"
	"github.com/google/subcommands"
)

func init() {
	commands = append(commands, &discCmd{})
}

type discCmd struct {
	index indexFlag
}

func (cmd *discCmd) Name() string {
	return "disc"
}

func (cmd *discCmd) Synopsis() string {
	return "Disconnect an NBD devices"
}

func (cmd *discCmd) Usage() string {
	return `Usage: nbd disc -index <n>

Disconnect an NBD device. If the given device is not connected, disc is a
no-op.
`
}

func (cmd *discCmd) SetFlags(fs *flag.FlagSet) {
	cmd.index.def = "none"
	fs.Var(&cmd.index, "index", "Index of NBD device")
}

func (cmd *discCmd) Execute(ctx context.Context, fs *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	if !cmd.index.set {
		log.Println("-index is required")
		return subcommands.ExitFailure
	}
	if err := nbdnl.Disconnect(cmd.index.val); err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	return subcommands.ExitSuccess
}
