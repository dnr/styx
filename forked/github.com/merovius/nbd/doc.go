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

// Package nbd implements the NBD network protocol.
//
// You can find a full description of the protocol at
// https://sourceforge.net/p/nbd/code/ci/master/tree/doc/proto.md
//
// This package implements both the client and the server side of the protocol,
// as well as (on Linux) utilities to hook up the kernel NBD client to use a
// server as a block device. The protocol is split into two phases: The
// handshake phase, which allows the client and server to negotiate their
// respective capabilities and what export to use. And the transmission phase,
// for actually reading/writing to the block device.
//
// The client side of the handshake is done with the Client type. Its methods
// can be used to list the exports a server provides and their respective
// capabilities. Its Go method enters transmission phase. The returned Export
// can then be passed to Configure (linux only) to hook it up to an NBD device
// (/dev/nbdX).
//
// The server side combines both handshake and transmission phase into the
// Serve or ListenAndServe functions. The user is expected to implement the
// Device interface to serve actual reads/writes. Under linux, the Loopback
// function serves as a convenient way to use a given Device as a block device.
package nbd

// BUG(1): BlockSizeConstraints are not yet enforced by the server.

// BUG(2): The server does not yet support FUA for direct IO.

// BUG(3): StartTLS is not supported yet.

// BUG(4): There is no way to declare a preferred block size for Loopback yet.

// BUG(5): Server flags are not yet set (or used) correctly.

// BUG(6): Structured replies are not yet supported.

// BUG(7): CMD_TRIM is not yet supported.

// BUG(8): Lame-duck mode (ESHUTDOWN) is not yet implemented.

// BUG(9): CMD_WRITE_ZEROES is not yet supported.

// BUG(10): Metadata querying is not yet supported.

// BUG(11): FLAG_ROTATIONAL is not yet supported.

// BUG(12): CMD_CACHE is not yet supported.
