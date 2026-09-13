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

// Package nbdnl controls the Linux NBD driver via netlink.
//
// It connects to the kernel netlink API via an unexported, lazily initialized
// connection. It can be used to connect any NBD server to an NBD-device
// (/dev/nbdX) which can then be used like a regular block device. In
// particular, this makes it possible to write userland block device drivers,
// by exposing an NBD-server over a local connection and connecting the kernel
// to it.
//
// This package provides the low-level netlink protocol, which in particular
// requires connections to be in the transmission phase (i.e. having done the
// NBD handshake phase or knowning the necessary information by other means).
// Most users will probably want to use the nbd package, which is more
// convenientand also implements handshaking.
package nbdnl

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/mdlayher/genetlink"
	"github.com/mdlayher/netlink"
)

const (
	familyName = "nbd"
	version    = 1
)

// IndexAny can be used to let the kernel choose a suitable device number (or
// create a new device if needed).
const IndexAny = ^uint32(0)

const (
	_ = iota
	cmdconnect
	cmdDisconnect
	cmdReconfigure
	_ // cmdLinkDead does not exist anymore
	cmdStatus
)

const (
	_ = iota
	attrIndex
	attrSizeBytes
	attrBlockSizeBytes
	attrTimeout
	attrServerFlags
	attrClientFlags
	attrSockets
	attrDeadconnTimeout
	attrDeviceList
)

// conn is a shared connection for all netlink operations. It gets lazily
// initialized on first use.
var conn struct {
	mu     sync.Mutex
	c      *genetlink.Conn
	family uint16
}

// dial initalizes conn, if needed.
func dial() error {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	var err error
	if conn.c == nil {
		conn.c, err = genetlink.Dial(nil)
		if err != nil {
			return err
		}
	}

	if conn.family == 0 {
		fam, err := conn.c.GetFamily(familyName)
		if err != nil {
			return err
		}
		if fam.Version < version {
			return fmt.Errorf("kernel does not support nbd-netlink v%d", version)
		}
		conn.family = fam.ID
	}
	return nil
}

// ConnectOption is an optional setting to configure the in-kernel NBD client.
type ConnectOption func(e *netlink.AttributeEncoder)

// WithBlockSize sets the block size used by the client to n.
func WithBlockSize(n uint64) ConnectOption {
	return func(e *netlink.AttributeEncoder) {
		e.Uint64(attrBlockSizeBytes, n)
	}
}

// WithTimeout sets the read-timeout for the NBD client to d.
func WithTimeout(d time.Duration) ConnectOption {
	return func(e *netlink.AttributeEncoder) {
		e.Uint64(attrTimeout, uint64(d/time.Second))
	}
}

// WithDeadconnTimeout sets the timeout after which the client considers a
// server unreachable to d.
func WithDeadconnTimeout(d time.Duration) ConnectOption {
	return func(e *netlink.AttributeEncoder) {
		e.Uint64(attrDeadconnTimeout, uint64(d/time.Second))
	}
}

// ClientFlags are flags configuring client behavior.
type ClientFlags uint64

const (
	// FlagDestroyOnDisconnect tells the client to delete the nbd device on
	// disconnect.
	FlagDestroyOnDisconnect ClientFlags = 1 << iota
	// FlagDisconnectOnClose tells the client to disconnect the nbd device on
	// close by last opener.
	FlagDisconnectOnClose
)

// ServerFlags specify what optional features the server supports.
type ServerFlags uint64

const (
	// FlagHasFlags is set if the server supports flags.
	FlagHasFlags ServerFlags = 1 << 0
	// FlagReadOnly is set if the export is read-only.
	FlagReadOnly ServerFlags = 1 << 1
	// FlagSendFlush is set if the exports supports the Flush command.
	FlagSendFlush ServerFlags = 1 << 2
	// FlagSendFUA is set if the export supports the Forced Unit Access command
	// flag.
	FlagSendFUA ServerFlags = 1 << 3
	// FlagSendTrim is set if the export supports the Trim command.
	FlagSendTrim ServerFlags = 1 << 5
	// FlagCanMulticonn is set if the export can serve multiple connections.
	FlagCanMulticonn ServerFlags = 1 << 8
)

// Connect instructs the kernel to connect the given set of sockets to the
// given NBD device number. socks must be NBD connections in transmission mode.
// cf can be used to configure client behavior and sf to specify the set of
// supported operations. If idx is IndexAny, the kernel chooses a device for us
// or creates one, if none is available.
func Connect(idx uint32, socks []*os.File, size uint64, cf ClientFlags, sf ServerFlags, opts ...ConnectOption) (uint32, error) {
	if err := dial(); err != nil {
		return 0, err
	}

	e := netlink.NewAttributeEncoder()
	if idx != IndexAny {
		e.Uint32(attrIndex, idx)
	}
	e.Uint64(attrSizeBytes, size)
	var sl []uint32
	for _, s := range socks {
		sl = append(sl, uint32(s.Fd()))
	}
	buf, err := encodeSockList(sl)
	if err != nil {
		return 0, err
	}
	e.Bytes(attrSockets, buf)
	e.Uint64(attrClientFlags, uint64(cf))
	e.Uint64(attrServerFlags, uint64(sf))
	for _, o := range opts {
		o(e)
	}
	body, err := e.Encode()
	if err != nil {
		return 0, err
	}
	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: cmdconnect,
			Version: 0,
		},
		Data: body,
	}
	msgs, err := conn.c.Execute(msg, conn.family, netlink.Request)
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		d, err := netlink.NewAttributeDecoder(m.Data)
		if err != nil {
			// TODO: this leaves the device in an undefined state
			return 0, err
		}
		for d.Next() {
			if d.Type() != attrIndex {
				continue
			}
			idx = d.Uint32()
		}
		if err := d.Err(); err != nil {
			return 0, err
		}
	}
	if idx == IndexAny {
		return 0, errors.New("no index returned by kernel")
	}
	return idx, nil
}

// Reconfigure reconfigures the given device. The arguments are equivalent to
// Configure, except that IndexAny is invalid for Reconfigure and WithBlockSize
// is ignored.
func Reconfigure(idx uint32, socks []*os.File, cf ClientFlags, sf ServerFlags, opts ...ConnectOption) error {
	if err := dial(); err != nil {
		return err
	}

	e := netlink.NewAttributeEncoder()
	e.Uint32(attrIndex, idx)
	var sl []uint32
	for _, s := range socks {
		sl = append(sl, uint32(s.Fd()))
	}
	buf, err := encodeSockList(sl)
	if err != nil {
		return err
	}
	e.Bytes(attrSockets, buf)
	e.Uint64(attrClientFlags, uint64(cf))
	e.Uint64(attrServerFlags, uint64(sf))
	for _, o := range opts {
		o(e)
	}
	body, err := e.Encode()
	if err != nil {
		return err
	}
	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: cmdReconfigure,
			Version: 0,
		},
		Data: body,
	}
	// Note: nbd_genl_reconfigure doesn't send a reply, so we need to set the
	// ACK flag here to request a reply from the transport.
	_, err = conn.c.Execute(msg, conn.family, netlink.Request|netlink.Acknowledge)
	return err
}

// Disconnect instructs the kernel to disconnect the given device.
func Disconnect(idx uint32) error {
	if err := dial(); err != nil {
		return err
	}

	e := netlink.NewAttributeEncoder()
	e.Uint32(attrIndex, idx)
	body, err := e.Encode()
	if err != nil {
		return err
	}
	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: cmdDisconnect,
			Version: 0,
		},
		Data: body,
	}
	// Note: nbd_genl_disconnect doesn't send a reply, so we need to set the ACK
	// flag here to request a reply from the transport.
	_, err = conn.c.Execute(msg, conn.family, netlink.Request|netlink.Acknowledge)
	return err
}

func encodeSockList(l []uint32) ([]byte, error) {
	const (
		sockItem = iota + 1
	)
	e := netlink.NewAttributeEncoder()
	for _, fd := range l {
		e.Do(sockItem, func() ([]byte, error) {
			const (
				sockFD = iota + 1
			)
			e := netlink.NewAttributeEncoder()
			e.Uint32(sockFD, fd)
			return e.Encode()
		})
	}
	return e.Encode()
}

// Status returns the status of the given NBD device.
func Status(idx uint32) (DeviceStatus, error) {
	li, err := status(idx)
	if err != nil {
		return DeviceStatus{}, err
	}
	i := sort.Search(len(li), func(i int) bool {
		return li[i].Index >= idx
	})
	if i < len(li) && li[i].Index == idx {
		return li[i], nil
	}
	return DeviceStatus{}, errors.New("device not found")
}

// StatusAll lists all NBD devices and their corresponding status.
func StatusAll() ([]DeviceStatus, error) {
	li, err := status(IndexAny)
	if err != nil {
		return nil, err
	}
	return li, nil
}

func status(idx uint32) ([]DeviceStatus, error) {
	if err := dial(); err != nil {
		return nil, err
	}

	e := netlink.NewAttributeEncoder()
	e.Uint32(attrIndex, idx)
	body, err := e.Encode()
	if err != nil {
		return nil, err
	}

	msg := genetlink.Message{
		Header: genetlink.Header{
			Command: cmdStatus,
			Version: 0,
		},
		Data: body,
	}
	msgs, err := conn.c.Execute(msg, conn.family, netlink.Request)
	if err != nil {
		return nil, err
	}
	var out []DeviceStatus
	for _, m := range msgs {
		d, err := netlink.NewAttributeDecoder(m.Data)
		if err != nil {
			return nil, err
		}
		for d.Next() {
			if d.Type() != attrDeviceList {
				continue
			}
			li, err := decodeDeviceList(d.Bytes())
			if err != nil {
				return nil, err
			}
			out = append(out, li...)
		}
		if err := d.Err(); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Index < out[j].Index
	})
	return out, nil
}

// DeviceStatus is the status of an NBD device.
type DeviceStatus struct {
	Index     uint32
	Connected bool
}

func decodeDeviceList(b []byte) ([]DeviceStatus, error) {
	const (
		deviceItem = iota + 1
	)
	var li []DeviceStatus
	d, err := netlink.NewAttributeDecoder(b)
	if err != nil {
		return nil, err
	}
	for d.Next() {
		if d.Type() != deviceItem {
			continue
		}
		it, err := decodeDeviceListItem(d.Bytes())
		if err != nil {
			return nil, err
		}
		li = append(li, it)
	}
	return li, d.Err()
}

func decodeDeviceListItem(b []byte) (DeviceStatus, error) {
	const (
		deviceIndex = iota + 1
		deviceConnected
	)
	var it DeviceStatus
	d, err := netlink.NewAttributeDecoder(b)
	if err != nil {
		return it, err
	}
	for d.Next() {
		switch d.Type() {
		case deviceIndex:
			it.Index = d.Uint32()
		case deviceConnected:
			it.Connected = d.Uint8() != 0
		}
	}
	return it, d.Err()
}
