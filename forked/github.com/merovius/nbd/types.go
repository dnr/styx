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
	"errors"
	"fmt"
	"strconv"
)

const (
	nbdMagic             = 0x4e42444d41474943
	optMagic             = 0x49484156454F5054
	repMagic             = 0x0003e889045565a9
	reqMagic             = 0x25609513
	simpleReplyMagic     = 0x67446698
	structuredReplyMagic = 0x668e33ef
	flagFixedNewstyle    = 1 << 0
	flagNoZeroes         = 1 << 1
	flagDefaults         = flagFixedNewstyle | flagNoZeroes
	maxOptionLength      = 4 << 10
)

type optionRequest interface {
	encode(*encoder)
	decode(*encoder, uint32) errno
	code() uint32
}

func decodeOption(e *encoder) (uint32, interface{}, errno) {
	magic := e.uint64()
	if magic != optMagic {
		e.check(fmt.Errorf("invalid option magic 0x%x", magic))
	}
	option := e.uint32()
	length := e.uint32()
	if length > maxOptionLength {
		return option, nil, errTooBig
	}
	var o interface{ decode(*encoder, uint32) errno }
	switch option {
	case cOptExportName:
		o = new(optExportName)
	case cOptAbort:
		o = new(optAbort)
	case cOptList:
		o = new(optList)
	case cOptInfo:
		o = &optInfo{done: false}
	case cOptGo:
		o = &optInfo{done: true}
	}
	if o == nil {
		return option, nil, errUnsup
	}
	return option, o, o.decode(e, length)
}

const (
	cOptExportName      = 1
	cOptAbort           = 2
	cOptList            = 3
	cOptStartTLS        = 5
	cOptInfo            = 6
	cOptGo              = 7
	cOptStructuredReply = 8
	cOptListMetaContext = 9
	cOptSetMetaContext  = 10
)

type optExportName struct {
	name string
}

func (o *optExportName) decode(e *encoder, l uint32) errno {
	name := make([]byte, l)
	e.read(name)
	o.name = string(name)
	return 0
}

type optAbort struct{}

func (o *optAbort) code() uint32 { return cOptAbort }

func (o *optAbort) encode(e *encoder) {}

func (o *optAbort) decode(e *encoder, l uint32) errno {
	if l != 0 {
		return errInvalid
	}
	return 0
}

type optList struct{}

func (o *optList) code() uint32 { return cOptList }

func (o *optList) decode(e *encoder, l uint32) errno {
	if l != 0 {
		return errInvalid
	}
	return 0
}

func (o *optList) encode(e *encoder) {}

type optInfo struct {
	done bool
	name string
	reqs []uint16
}

func (o *optInfo) code() uint32 {
	if o.done {
		return cOptGo
	}
	return cOptInfo
}

func (o *optInfo) decode(e *encoder, l uint32) errno {
	nlen := e.uint32()
	if l < 6 || nlen > l-6 {
		return errInvalid
	}
	name := make([]byte, nlen)
	e.read(name)
	o.name = string(name)
	nreqs := e.uint16()
	if (l-nlen-6)%2 != 0 || (l-nlen-6)/2 != uint32(nreqs) {
		return errInvalid
	}
	for ; nreqs > 0; nreqs-- {
		o.reqs = append(o.reqs, e.uint16())
	}
	return 0
}

func (o *optInfo) encode(e *encoder) {
	e.writeUint32(uint32(len(o.name)))
	e.writeString(o.name)
	e.writeUint16(uint16(len(o.reqs)))
	for _, r := range o.reqs {
		e.writeUint16(r)
	}
}

type errno uint32

const (
	_ errno = (1 << 31) + iota
	errUnsup
	errPolicy
	errInvalid
	errPlatform
	errTLSReqd
	errUnknown
	errShutdown
	errBlockSizeReqd
	errTooBig
)

func (e errno) String() string {
	switch e {
	case errUnsup:
		return "ERR_UNSUP"
	case errPolicy:
		return "ERR_POLICY"
	case errInvalid:
		return "ERR_INVALID"
	case errPlatform:
		return "ERR_PLATFORM"
	case errTLSReqd:
		return "ERR_TLS_REQD"
	case errUnknown:
		return "ERR_UNKNOWN"
	case errShutdown:
		return "ERR_SHUTDOWN"
	case errBlockSizeReqd:
		return "ERR_BLOCK_SIZE_REQD"
	case errTooBig:
		return "ERR_TOO_BIG"
	default:
		return "0x" + strconv.FormatUint(uint64(e), 16)
	}
}

type optionReply interface {
	code() uint32
	encode(*encoder)
	decode(*encoder, uint32)
}

func encodeReply(e *encoder, option uint32, reply optionReply) {
	e.writeUint64(repMagic)
	e.writeUint32(option)
	e.writeUint32(reply.code())
	e.buf = []byte{}
	var buf []byte
	reply.encode(e)
	buf, e.buf = e.buf, nil
	e.writeUint32(uint32(len(buf)))
	e.write(buf)
}

const (
	cRepAck    = 1
	cRepServer = 2
	cRepInfo   = 3
)

type repAck struct{}

func (r *repAck) code() uint32 { return cRepAck }

func (r *repAck) encode(*encoder) {}

func (r *repAck) decode(e *encoder, l uint32) {
	if l != 0 {
		e.check(errors.New("invalid ack response"))
	}
}

type repServer struct {
	name    string
	details string
}

func (r *repServer) code() uint32 { return cRepServer }

func (r *repServer) encode(e *encoder) {
	e.writeUint32(uint32(len(r.name)))
	e.writeString(r.name)
	e.writeString(r.details)
}

func (r *repServer) decode(e *encoder, l uint32) {
	if l < 4 {
		e.check(errors.New("invalid server response"))
	}
	length := e.uint32()
	if length > l-4 {
		e.check(errors.New("invalid server response"))
	}
	b := make([]byte, l-4)
	e.read(b)
	r.name = string(b[:length])
	r.details = string(b[length:])
}

const (
	cInfoExport      = 0
	cInfoName        = 1
	cInfoDescription = 2
	cInfoBlockSize   = 3
)

func decodeInfo(e *encoder, l uint32) optionReply {
	if l < 2 {
		e.check(errors.New("invalid length for info reply"))
	}
	code := e.uint16()
	var rep optionReply
	switch code {
	case cInfoExport:
		rep = new(infoExport)
	case cInfoName:
		rep = new(infoName)
	case cInfoDescription:
		rep = new(infoDescription)
	case cInfoBlockSize:
		rep = new(infoBlockSize)
	default:
		e.discard(l - 2)
		return nil
	}
	rep.decode(e, l-2)
	return rep
}

type infoExport struct {
	size  uint64
	flags uint16
}

func (r *infoExport) code() uint32 { return cRepInfo }

func (r *infoExport) encode(e *encoder) {
	e.writeUint16(cInfoExport)
	e.writeUint64(r.size)
	e.writeUint16(r.flags)
}

func (r *infoExport) decode(e *encoder, l uint32) {
	if l != 10 {
		e.check(errors.New("invalid length for info reply"))
	}
	r.size = e.uint64()
	r.flags = e.uint16()
}

type infoName struct {
	name string
}

func (r *infoName) code() uint32 { return cRepInfo }

func (r *infoName) encode(e *encoder) {
	e.writeUint16(cInfoName)
	e.writeString(r.name)
}

func (r *infoName) decode(e *encoder, l uint32) {
	if l > (4 << 10) {
		e.check(errors.New("name too large"))
	}
	b := make([]byte, l)
	e.read(b)
	r.name = string(b)
}

type infoDescription struct {
	description string
}

func (r *infoDescription) code() uint32 { return cRepInfo }

func (r *infoDescription) encode(e *encoder) {
	e.writeUint16(cInfoDescription)
	e.writeString(r.description)
}

func (r *infoDescription) decode(e *encoder, l uint32) {
	if l > (4 << 10) {
		e.check(errors.New("description too large"))
	}
	b := make([]byte, l)
	e.read(b)
	r.description = string(b)
}

type infoBlockSize struct {
	min       uint32
	preferred uint32
	max       uint32
}

func (r *infoBlockSize) code() uint32 { return cRepInfo }

func (r *infoBlockSize) encode(e *encoder) {
	e.writeUint16(cInfoBlockSize)
	e.writeUint32(r.min)
	e.writeUint32(r.preferred)
	e.writeUint32(r.max)
}

func (r *infoBlockSize) decode(e *encoder, l uint32) {
	if l != 12 {
		e.check(errors.New("invalid length for block size info"))
	}
	r.min = e.uint32()
	r.preferred = e.uint32()
	r.max = e.uint32()
}

type repError struct {
	errno errno
	msg   string
}

func (r *repError) code() uint32 { return uint32(r.errno) }

func (r *repError) encode(e *encoder) {}

func (r *repError) decode(e *encoder, l uint32) {
	if l > (4 << 20) {
		e.check(errors.New("error string too larg"))
	}
	b := make([]byte, l)
	e.read(b)
	r.msg = string(b)
}

func (r *repError) Error() string {
	if r.msg != "" {
		return fmt.Sprintf("%s (%s)", r.msg, r.errno)
	}
	return r.errno.String()
}

const (
	cmdRead        = 0
	cmdWrite       = 1
	cmdDisc        = 2
	cmdFlush       = 3
	cmdTrim        = 4
	cmdCache       = 5
	cmdWriteZeroes = 6
	cmdBlockStatus = 7
	cmdResize      = 8
)

// Errno is an error code suitable to be sent over the wire. It mostly
// corresponds to syscall.Errno, though the constants in this package are
// specified and so are the only ones to be safe to be sent over the wire and
// understood by all NBD servers/clients.
type Errno uint32

// See https://manpages.debian.org/stretch/manpages-dev/errno.3.en.html for a
// description of error numbers.
const (
	EPERM     Errno = 1
	EIO       Errno = 5
	ENOMEM    Errno = 12
	EINVAL    Errno = 22
	ENOSPC    Errno = 28
	EOVERFLOW Errno = 75
	ESHUTDOWN Errno = 108
)

var errStr = map[Errno]string{
	EPERM:     "Operation not permitted",
	EIO:       "Input/output error",
	ENOMEM:    "Cannot allocate memory",
	EINVAL:    "Invalid argument",
	ENOSPC:    "No space left on device",
	EOVERFLOW: "Value too large for defined data type",
	ESHUTDOWN: "Cannot send after transport endpoint shutdown",
}

func (e Errno) Error() string {
	if msg, ok := errStr[e]; ok {
		return msg
	}
	return fmt.Sprintf("NBD_ERROR(%d)", uint32(e))
}

// Errno returns e.
func (e Errno) Errno() Errno {
	return e
}

type errf struct {
	errno Errno
	error
}

func (e errf) Errno() Errno {
	return e.errno
}

// Errorf returns an error implementing Error, returning code from Errno.
func Errorf(code Errno, msg string, v ...interface{}) Error {
	if len(v) > 0 {
		return errf{code, fmt.Errorf(msg, v...)}
	}
	return errf{code, errors.New(msg)}
}

const (
	cmdFlagFUA    = 1 << 0
	cmdFlagNoHole = 1 << 1
	cmdFlagDF     = 1 << 2
	cmdFlagReqOne = 1 << 3
)

const (
	replyFlagDone = 1 << 0
)

const (
	replyTypeNone        = 0
	replyTypeOffsetData  = 1
	replyTypeOffsetHole  = 2
	replyTypeBlockStatus = 5
	replyTypeError       = (1 << 15) + 1
	replyTypeErrorOffset = (1 << 15) + 2
)

type request struct {
	flags  uint16
	typ    uint16
	handle uint64
	offset uint64
	length uint32
	data   []byte
}

func (r *request) encode(e *encoder) {
	e.writeUint32(reqMagic)
	e.writeUint16(r.flags)
	e.writeUint16(r.typ)
	e.writeUint64(r.handle)
	e.writeUint64(r.offset)
	e.writeUint32(uint32(len(r.data)))
	e.write(r.data)
}

func (r *request) decode(e *encoder) Error {
	if e.uint32() != reqMagic {
		e.check(errors.New("invalid magic for request"))
	}
	r.flags = e.uint16()
	r.typ = e.uint16()
	r.handle = e.uint64()
	r.offset = e.uint64()
	r.length = e.uint32()
	if r.offset&(1<<63) != 0 {
		return EOVERFLOW
	}
	if r.typ != cmdWrite {
		return nil
	}
	if r.length > 4<<20 {
		e.discard(r.length)
		return EOVERFLOW
	}
	buf := make([]byte, r.length)
	e.read(buf)
	r.data = buf
	return nil
}

type simpleReply struct {
	errno  uint32
	handle uint64
	data   []byte

	length uint32
}

func (r *simpleReply) encode(e *encoder) {
	e.writeUint32(simpleReplyMagic)
	e.writeUint32(r.errno)
	e.writeUint64(r.handle)
	e.write(r.data)
}

func (r *simpleReply) decode(e *encoder) Error {
	if e.uint32() != simpleReplyMagic {
		e.check(errors.New("invalid magic for reply"))
	}
	r.handle = e.uint64()
	buf := make([]byte, r.length)
	e.read(buf)
	return nil
}

type structuredReply struct {
	flags  uint16
	typ    uint16
	handle uint64
	length uint32
	data   []byte
}

func (r *structuredReply) encode(e *encoder) {
	e.writeUint32(structuredReplyMagic)
	e.writeUint16(r.flags)
	e.writeUint16(r.typ)
	e.writeUint64(r.handle)
	e.writeUint32(r.length)
	e.write(r.data)
}

func (r *structuredReply) decode(e *encoder) Error {
	if e.uint64() != structuredReplyMagic {
		e.check(errors.New("invalid magic for reply"))
	}
	r.flags = e.uint16()
	r.typ = e.uint16()
	r.handle = e.uint64()
	r.length = e.uint32()
	if r.length > 4<<20 {
		e.discard(r.length)
		return EOVERFLOW
	}
	buf := make([]byte, r.length)
	e.read(buf)
	r.data = buf
	return nil
}
