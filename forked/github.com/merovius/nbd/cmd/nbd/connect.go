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
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/Merovius/nbd"
	"github.com/google/subcommands"
)

func init() {
	commands = append(commands, &connectCmd{})
}

type connectCmd struct {
	addr   string
	unix   bool
	export string
}

func (cmd *connectCmd) Name() string {
	return "connect"
}

func (cmd *connectCmd) Synopsis() string {
	return "connect a file as a block device"
}

func (cmd *connectCmd) Usage() string {
	return `Usage: nbd connect -addr <addr> [-unix]

Connect a server to an NBD device node.
`
}

func (cmd *connectCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&cmd.export, "export", "", "Export to use. If not provided, the default is used")
	fs.StringVar(&cmd.addr, "addr", "localhost:10809", "Address to listen on")
	fs.BoolVar(&cmd.unix, "unix", false, "Serve on a unix domain socket")
}

func (cmd *connectCmd) Execute(ctx context.Context, fs *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	if fs.NArg() != 0 {
		log.Print(cmd.Usage())
		return subcommands.ExitUsageError
	}

	network := "tcp"
	if cmd.unix {
		network = "unix"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	c, err := new(net.Dialer).DialContext(ctx, network, cmd.addr)
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	defer c.Close()

	var sock *os.File
	switch c := c.(type) {
	case *net.TCPConn:
		sock, err = c.File()
	case *net.UnixConn:
		sock, err = c.File()
	default:
		err = errors.New("could not get file descriptor: unknown connection type")
	}
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	defer sock.Close()

	cl, err := nbd.ClientHandshake(ctx, c)
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	exp, err := cl.Go("")
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	n, err := nbd.Configure(exp, sock)
	if err != nil {
		log.Println(err)
		return subcommands.ExitFailure
	}
	fmt.Printf("/dev/nbd%d\n", n)
	return subcommands.ExitSuccess
}
