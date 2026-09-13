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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

// Export specifies the data needed for the NBD network protocol.
type Export struct {
	Name        string
	Description string
	Size        uint64
	Flags       uint16 // TODO: Determine Flags from Device.
	BlockSizes  *BlockSizeConstraints
	Device      Device
}

// BlockSizeConstraints optionally specifies possible block sizes for a given
// export.
type BlockSizeConstraints struct {
	Min       uint32
	Preferred uint32
	Max       uint32
}

var defaultBlockSizes = BlockSizeConstraints{1, 4096, 0xffffffff}

type connParameters struct {
	Export     Export
	BlockSizes BlockSizeConstraints
}

func serverHandshake(rw io.ReadWriter, exp []Export) (connParameters, error) {
	parms := connParameters{
		BlockSizes: defaultBlockSizes,
	}
	return parms, do(rw, func(e *encoder) {
		e.writeUint64(nbdMagic)
		e.writeUint64(optMagic)
		e.writeUint16(flagDefaults)
		clientFlags := e.uint32()

		if clientFlags & ^uint32(flagDefaults) != 0 {
			e.check(fmt.Errorf("handshake aborted due to unknown handshake flags 0x%d", clientFlags & ^uint32(flagDefaults)))
		}
		if clientFlags != flagDefaults {
			e.check(fmt.Errorf("refusing deprecated handshake flags 0x%x", clientFlags))
		}

		for {
			code, o, err := decodeOption(e)
			if err != 0 {
				encodeReply(e, code, &repError{err, ""})
				continue
			}
			switch o := o.(type) {
			case *optExportName:
				var ok bool
				parms.Export, ok = findExport(o.name, exp)
				if !ok {
					encodeReply(e, code, &repError{errUnknown, ""})
					continue
				}
				e.writeUint64(parms.Export.Size)
				e.writeUint16(parms.Export.Flags)
				return
			case *optAbort:
				encodeReply(e, code, &repAck{})
				e.check(errors.New("client aborted negotiation"))
			case *optList:
				for _, ex := range exp {
					encodeReply(e, code, &repServer{ex.Name, ""})
				}
				encodeReply(e, code, &repAck{})
			case *optInfo:
				var ok bool
				parms.Export, ok = findExport(o.name, exp)
				if !ok {
					encodeReply(e, code, &repError{errUnknown, ""})
					continue
				}
				encodeReply(e, code, &infoExport{parms.Export.Size, parms.Export.Flags})
				for _, r := range o.reqs {
					switch r {
					case cInfoExport:
						// already sent
					case cInfoName:
						encodeReply(e, code, &infoName{parms.Export.Name})
					case cInfoDescription:
						encodeReply(e, code, &infoDescription{parms.Export.Description})
					case cInfoBlockSize:
						if parms.Export.BlockSizes == nil {
							break
						}
						if o.done {
							parms.BlockSizes = *parms.Export.BlockSizes
						}
						encodeReply(e, code, &infoBlockSize{
							parms.BlockSizes.Min,
							parms.BlockSizes.Preferred,
							parms.BlockSizes.Max,
						})
					}
				}
				encodeReply(e, code, &repAck{})
				if o.done {
					return
				}
			}
		}
	})
}

// Client performs the client-side of the NBD network protocol handshake and
// can be used to query information about the exports from a server.
type Client struct {
	rw     io.ReadWriteCloser
	closed bool
}

// ClientHandshake starts the client-side of the NBD handshake over c.
func ClientHandshake(ctx context.Context, c net.Conn) (*Client, error) {
	rw := wrapConn(ctx, c)
	cl := &Client{rw, false}
	return cl, do(rw, func(e *encoder) {
		if e.uint64() != nbdMagic {
			e.check(errors.New("invalid magic from server"))
		}
		if e.uint64() != optMagic {
			e.check(errors.New("invalid magic from server"))
		}
		serverFlags := e.uint16()
		if serverFlags&flagDefaults != flagDefaults {
			e.check(errors.New("refusing deprecated handshake flags"))
		}
		e.writeUint32(flagDefaults)
	})
}

func (c *Client) checkClosed(e *encoder) {
	if c.closed {
		e.check(errors.New("use of closed client"))
	}
}

// send sends an option request to the server.
func (c *Client) send(e *encoder, o optionRequest) {
	c.checkClosed(e)
	e.writeUint64(optMagic)
	e.writeUint32(o.code())
	e.buf = []byte{}
	o.encode(e)
	buf := e.buf
	e.buf = nil
	e.writeUint32(uint32(len(buf)))
	e.write(buf)
}

// recv receives an option reply from the server.
func (c *Client) recv(e *encoder, code uint32) optionReply {
	c.checkClosed(e)
	if e.uint64() != repMagic {
		e.check(errors.New("invalid reply magic from server"))
	}
	if e.uint32() != code {
		e.check(errors.New("server responded to wrong option"))
	}
	code = e.uint32()
	length := e.uint32()
	var rep optionReply
	switch code {
	case cRepAck:
		rep = new(repAck)
	case cRepServer:
		rep = new(repServer)
	case cRepInfo:
		return decodeInfo(e, length)
	default:
		if code&(1<<31) != 0 {
			rep = &repError{errno: errno(code)}
			rep.decode(e, length)
			e.check(rep.(error))
		} else {
			e.check(fmt.Errorf("unknown response code 0x%x", code))
		}
		return nil
	}
	rep.decode(e, length)
	return rep

}

// Abort aborts the handshake. c should not be used after Abort returns.
func (c *Client) Abort() error {
	return do(c.rw, func(e *encoder) {
		c.send(e, &optAbort{})
		rep := c.recv(e, cOptAbort)
		c.close()
		switch rep.(type) {
		case *repAck:
		default:
			e.check(errors.New("invalid response to abort request"))
		}
	})
}

// List returns the names of exports the server is providing.
func (c *Client) List() ([]string, error) {
	var list []string
	err := do(c.rw, func(e *encoder) {
		c.send(e, &optList{})
		for {
			rep := c.recv(e, cOptList)
			switch rep := rep.(type) {
			case *repAck:
				return
			case *repServer:
				list = append(list, rep.name)
			default:
				e.check(errors.New("invalid response to list request"))
			}
		}
	})
	return list, err
}

// into sends an NBD_OPT_INFO (if done == false) or NBD_OPT_GO (if done ==
// true) request and returns the export data returned by the server.
func (c *Client) info(exportName string, done bool) (Export, error) {
	var ex Export
	err := do(c.rw, func(e *encoder) {
		reqs := []uint16{cInfoExport, cInfoName, cInfoDescription, cInfoBlockSize}
		c.send(e, &optInfo{done, exportName, reqs})
		code := uint32(cOptInfo)
		if done {
			code = cOptGo
		}
		for {
			rep := c.recv(e, code)
			switch rep := rep.(type) {
			case *repAck:
				return
			case *infoExport:
				ex.Size = rep.size
				ex.Flags = rep.flags
			case *infoName:
				ex.Name = rep.name
			case *infoDescription:
				ex.Description = rep.description
			case *infoBlockSize:
				ex.BlockSizes = &BlockSizeConstraints{
					Min:       rep.min,
					Preferred: rep.preferred,
					Max:       rep.max,
				}
			default:
				e.check(errors.New("invalid response to info request"))
			}
		}
	})
	return ex, err
}

// Info requests information about the export identified by exportName. If
// exportName is the empty string, the default export will be queried.
func (c *Client) Info(exportName string) (Export, error) {
	return c.info(exportName, false)
}

// Go terminates the handshake phase of the NBD protocol, opening the export
// identified by exportName. If exportName is the empty string, the default
// export will be used. c should not be used after Go returns.
func (c *Client) Go(exportName string) (Export, error) {
	ex, err := c.info(exportName, true)
	c.close()
	return ex, err
}

// close marks the Client closed, preventing it from further use, and cleans up
// the connection wrapper. It does not close the wrapped connection.
func (c *Client) close() error {
	c.closed = true
	return c.rw.Close()
}

// findExport searches the list of exports for one with the given name. If name
// is empty, it returns the first export. findExport performs a linear search,
// so it doesn't scale to a large number of exports, but we assume for now that
// that's not a practical problem.
func findExport(name string, exp []Export) (Export, bool) {
	if len(exp) > 0 && name == "" {
		return exp[0], true
	}
	for _, e := range exp {
		if e.Name == name {
			return e, true
		}
	}
	return Export{}, false
}

// do wraps rw for easy en-/decoding of binary data. It creates an *encoder and
// calls f with that. The process uses panic/recover for error handling, so e
// should never be passed to a different goroutine.
func do(rw io.ReadWriter, f func(e *encoder)) (err error) {
	sentinel := new(uint8)
	defer func() {
		if v := recover(); v != nil && v != sentinel {
			panic(v)
		}
	}()
	check := func(e error) {
		if e != nil {
			err = e
			panic(sentinel)
		}
	}
	f(&encoder{rw, nil, check})
	return err
}

// encoder provides helper methods for easy de-/encoding of binary data.
// If an error occurs, it calls check, which is expected to panic if its
// non-nil. If buf is non-nil, the encoder won't write to rw directly, but
// append to buf. That way, nested messages can be buffered before writing them
// out, to determine their length.
type encoder struct {
	rw    io.ReadWriter
	buf   []byte
	check func(error)
}

func (e *encoder) write(b []byte) {
	if e.buf != nil {
		e.buf = append(e.buf, b...)
		return
	}
	_, err := e.rw.Write(b)
	e.check(err)
}

func (e *encoder) writeString(s string) {
	if e.buf != nil {
		e.buf = append(e.buf, s...)
		return
	}
	var err error
	if sw, ok := e.rw.(interface{ WriteString(string) (int, error) }); ok {
		_, err = sw.WriteString(s)
	} else {
		_, err = e.rw.Write([]byte(s))
	}
	e.check(err)
}

func (e *encoder) read(b []byte) {
	_, err := io.ReadFull(e.rw, b)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	e.check(err)
}

func (e *encoder) discard(n uint32) {
	buf := make([]byte, 512)
	for n > 0 {
		if n > uint32(len(buf)) {
			buf = buf[:n]
		}
		e.read(buf)
		n -= uint32(len(buf))
	}
}

func (e *encoder) uint16() uint16 {
	var b [2]byte
	e.read(b[:])
	return binary.BigEndian.Uint16(b[:])
}

func (e *encoder) uint32() uint32 {
	var b [4]byte
	e.read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

func (e *encoder) uint64() uint64 {
	var b [8]byte
	e.read(b[:])
	return binary.BigEndian.Uint64(b[:])
}

func (e *encoder) writeUint16(v uint16) {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	e.write(b[:])
}

func (e *encoder) writeUint32(v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	e.write(b[:])
}

func (e *encoder) writeUint64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	e.write(b[:])
}
