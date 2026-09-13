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
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Error combines the normal error interface with an Errno method, that returns
// an NBD error number. All of Device's methods should return an Error -
// otherwise, EIO is assumed as the error number.
type Error interface {
	Error() string
	Errno() Errno
}

// Device is the interface that should be implemented to expose an NBD device
// to the network or the kernel. Errors returned should implement Error -
// otherwise, EIO is assumed as the error number.
type Device interface {
	io.ReaderAt
	io.WriterAt
	// Sync should block until all previous writes where written to persistent
	// storage and return any errors that occured.
	Sync() error
}

// ListenAndServe starts listening on the given network/address and serves the
// given exports, the first of which will serve as the default. It starts a new
// goroutine for each connection. ListenAndServe only returns when ctx is
// cancelled or an unrecoverable error occurs. Either way, it will wait for all
// connections to terminate first.
func ListenAndServe(ctx context.Context, network, addr string, exp ...Export) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	ctxCancel, cancel := context.WithCancel(ctx)
	defer cancel()

	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctxCancel.Done()
		l.Close()
	}()

	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			Serve(ctx, c, exp...)
			c.Close()
		}()
	}
}

// Serve serves the given exports on c. The first export is used as a default.
// Serve returns after ctx is cancelled or an error occurs.
func Serve(ctx context.Context, c net.Conn, exp ...Export) error {
	parms, err := serverHandshake(c, exp)
	if err != nil {
		return err
	}
	return serve(ctx, c, parms)
}

// serve serves nbd requests for a connection in transmission mode using p. It
// returns after ctx is cancelled or an error occurs.
func serve(ctx context.Context, c net.Conn, p connParameters) error {
	rw := wrapConn(ctx, c)
	defer rw.Close()
	return do(rw, func(e *encoder) {
		var req request
		for {
			if err := req.decode(e); err != nil {
				respondErr(e, req.handle, err)
				continue
			}
			switch req.typ {
			case cmdRead:
				if req.length == 0 {
					respondErr(e, req.handle, EINVAL)
					continue
				}
				buf := make([]byte, req.length)
				_, err := p.Export.Device.ReadAt(buf, int64(req.offset))
				if err != nil {
					respondErr(e, req.handle, err)
					continue
				}
				(&simpleReply{0, req.handle, buf, 0}).encode(e)
			case cmdWrite:
				if req.length == 0 {
					respondErr(e, req.handle, EINVAL)
					continue
				}
				_, err := p.Export.Device.WriteAt(req.data, int64(req.offset))
				if err != nil {
					respondErr(e, req.handle, err)
					continue
				}
				(&simpleReply{0, req.handle, nil, 0}).encode(e)
			case cmdDisc:
				return
			case cmdFlush:
				if req.length != 0 || req.offset != 0 {
					respondErr(e, req.handle, EINVAL)
					continue
				}
				err := p.Export.Device.Sync()
				if err != nil {
					respondErr(e, req.handle, err)
					continue
				}
				(&simpleReply{0, req.handle, nil, 0}).encode(e)
			default:
				respondErr(e, req.handle, EINVAL)
			}
		}
	})
}

// respondErr writes an error respons to e, based on handle an err.
func respondErr(e *encoder, handle uint64, err error) {
	code := EIO
	if e, ok := err.(Error); ok {
		code = e.Errno()
	}
	rep := simpleReply{
		errno:  uint32(code),
		handle: handle,
		length: 0,
	}
	rep.encode(e)
}

// ctxRW wraps a net.Conn to respect context cancellation. It does so by
// starting a goroutine that sets the connection's read/write deadline in the
// past whenever the context is cancelled.
type ctxRW struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	c      net.Conn
	done   <-chan struct{}
}

// wrapConn wraps a connection in a ctxRW.
func wrapConn(ctx context.Context, c net.Conn) io.ReadWriteCloser {
	// Note: cancel is called by Close().
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		c.SetDeadline(time.Now())
	}()
	return &ctxRW{ctx, cancel, c, done}
}

// Read implements io.Reader. It returns context.Cause(ctx) if the read was
// aborted due to context cancellation.
func (rw *ctxRW) Read(p []byte) (n int, err error) {
	n, err = rw.c.Read(p)
	if e := context.Cause(rw.ctx); e != nil {
		err = e
	}
	return n, err
}

// Write implements io.Writer. It returns context.Cause(ctx) if the write was
// aborted due to context cancellation.
func (rw *ctxRW) Write(p []byte) (n int, err error) {
	n, err = rw.c.Write(p)
	if e := context.Cause(rw.ctx); e != nil {
		err = e
	}
	return n, err
}

// Close implements io.Closer. It cleans up the resources associated with the
// ctxRW, but not the wrapped net.Conn. The wrapped net.Conn must be closed by
// the caller separately, otherwise any pending read/write operation may be left
// running indefinitely.
func (rw *ctxRW) Close() error {
	rw.cancel(errors.New("wrapped connection was closed"))
	<-rw.done
	return nil
}
