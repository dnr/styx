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
	"fmt"
	"log"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/Merovius/nbd/nbdnl"
	"github.com/google/subcommands"
)

func init() {
	commands = append(commands, &listCmd{})
}

type listCmd struct {
}

func (cmd *listCmd) Name() string {
	return "list"
}

func (cmd *listCmd) Synopsis() string {
	return "list NBD devices and their status"
}

func (cmd *listCmd) Usage() string {
	return `Usage: nbd list

List NBD devices and their status
`
}

func (cmd *listCmd) SetFlags(fs *flag.FlagSet) {
}

func (cmd *listCmd) Execute(ctx context.Context, fs *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	st, err := nbdnl.StatusAll()
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	sort.Slice(st, func(i, j int) bool { return st[i].Index < st[j].Index })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "Device\tConnected\n")
	for _, s := range st {
		fmt.Fprintf(w, "/dev/nbd%d\t%v\n", s.Index, s.Connected)
	}
	w.Flush()
	return subcommands.ExitSuccess
}
