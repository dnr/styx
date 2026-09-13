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

package nbd

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/Merovius/nbd/nbdnl"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

// Configure passes the given set of sockets to the kernel to provide them as
// an NBD device. socks must be connected to the same server (which must
// support multiple connections) and be in transmission phase. It returns the
// device-numbers that was chosen by the kernel or any error. You can then use
// /dev/nbdX as a block device. Use nbdnl.Disconnect to disconnect the device
// once you're done with it.
//
// This is a Linux-only API.
func Configure(e Export, socks ...*os.File) (uint32, error) {
	var opts []nbdnl.ConnectOption
	if e.BlockSizes != nil {
		opts = append(opts, nbdnl.WithBlockSize(uint64(e.BlockSizes.Preferred)))
	}
	return nbdnl.Connect(nbdnl.IndexAny, socks, e.Size, 0, nbdnl.ServerFlags(e.Flags), opts...)
}

// Loopback serves d on a private socket, passing the other end to the kernel
// to connect to an NBD device. It returns the device-number that the kernel
// chose. wait should be called to check for errors from serving the device. It
// blocks until ctx is cancelled or an error occurs (so it behaves like Serve).
// When ctx is cancelled, the device will be disconnected, and any error
// encountered while disconnecting will be returned by wait.
//
// This is a Linux-only API.
func Loopback(ctx context.Context, d Device, size uint64) (idx uint32, wait func() error, err error) {
	sp, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return 0, nil, err
	}
	exp := Export{
		Size:       size,
		Device:     d,
		BlockSizes: &defaultBlockSizes,
		Flags:      uint16(nbdnl.FlagHasFlags | nbdnl.FlagSendFlush),
	}

	client, server := os.NewFile(uintptr(sp[0]), "client"), os.NewFile(uintptr(sp[1]), "server")
	serverc, err := net.FileConn(server)
	server.Close()
	if err != nil {
		client.Close()
		return 0, nil, err
	}

	idx, err = Configure(exp, client)
	if err != nil {
		client.Close()
		return 0, nil, err
	}

	var eg errgroup.Group
	eg.Go(func() error {
		return serve(ctx, serverc, connParameters{exp, defaultBlockSizes})
	})
	wait = func() error {
		err := eg.Wait()
		// canceling the context is the only way for Loopback to return, so do
		// not consider them errors.
		if err == context.Canceled || err == context.DeadlineExceeded {
			err = nil
		}
		if e := nbdnl.Disconnect(idx); e != nil && err == nil {
			err = fmt.Errorf("failed to disconnect device: %w", e)
		}
		if e := client.Close(); e != nil && err == nil {
			err = fmt.Errorf("failed to close client socket: %w", e)
		}
		if e := serverc.Close(); e != nil && err == nil {
			err = fmt.Errorf("failed to close server connection: %w", e)
		}
		return err
	}
	return idx, wait, nil
}
