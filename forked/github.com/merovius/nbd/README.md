# Go implementation of the Linux Network Block Device protocol

This repository contains Go implementations of

* [The NBD network protocol][nbd-proto] (both client and server).
* The [Netlink API for NBD][nbd-netlink-h], to configure the in-kernel NBD
  client.
* [A basic CLI tool][nbd-tool] that uses these implementations.

It's currently in beta and thus there are a bunch of known (and unknown)
issues. Check the ["beta" label][beta-issues] for known problems. It is
particularly interesting if you require features that are not yet supported.
Please comment/vote on the corresponding issue.

# Installation

Currently, this is pre-release, so the only way to install is from source. To
do that, use

```
go get -u github.com/Merovius/nbd
```

# Using the library

There are two packages:
* [nbd][godoc-nbd], containing the client and server implementations of the
  network protocol, as well as some convenience functions for
  [nbdnl][godoc-nbdnl]. The network protocol is used as a handshake between
  client and server, to negotiate optional features and other options. Under
  Linux, there are also a couple of functions provided to easily hook up a
  `Device` implementation and use it as a block device.
* [nbdnl][godoc-nbdnl], containing an implementation of the NBD generic netlink
  family, based on Matt Layher's [genetlink package][godoc-genetlink]. This
  package can only be used on Linux; you should guard any usage with
  corresponding build tags.

The main usecase of this library is fuzzing code that tries to provide durable
filesystem-operations. It allows you to implement aribtrary failure modes of a
block device and then create any filesystem you'd like to test on it. For
example, to fuzz for crash-resistence, you can have the block device return
errors on any write-operations after an arbitrary point in time and then
repeatedly mount a filesystem, run a bunch of application code, simulate a
crash and then check invariants (after re-mounting). This can provide some
confidence (though no guarantees) that your code works with actual
filesystem-implementations.

Note, that any code that wants to configure the in-kernel NBD client has to be
privileged (the process needs to have `CAP_SYS_ADMIN`).

# NBD tool

This repo contains basic CLI tool to configure/serve/connect to NBD devices. You can install it via

```
go get -u github.com/Merovius/nbd/cmd/nbd
```

To see what it can do, use `nbd help`. Note, that most of the useful commands
require root (or, more specifically, `CAP_SYS_ADMIN`) to work.

One of the most useful subcommands is `lo`, which can be used to use a file as
a block device (similarly to `losetup`). It *also* supports toggling write-only
mode of the device via a unix signal, though, which can be used to test the
durability of software not written in Go. Refer to `nbd help lo` for details.

# License

```
Copyright 2018 Axel Wagner

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

[beta-issues]: https://github.com/Merovius/nbd/issues?q=is%3Aissue+is%3Aopen+label%3Abeta
[nbd-proto]: https://sourceforge.net/p/nbd/code/ci/master/tree/doc/proto.md
[nbd-netlink-h]: https://github.com/torvalds/linux/blob/master/include/uapi/linux/nbd-netlink.h
[nbd-tool]: #nbd-tool
[godoc-nbd]: https://godoc.org/github.com/Merovius/nbd
[godoc-nbdnl]: https://godoc.org/github.com/Merovius/nbdnl
[godoc-genetlink]: https://godoc.org/github.com/mdlayher/genetlink
