//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bufio"
	"github.com/pkg/errors"
	"io"
	"strings"
)

// MsgReader provides a structured way of streaming an LLRP message.
//
// This reader provides a simplified model to access message data fields and parameters,
// but relies on exclusive control of the underlying data source,
// so if you use it, you must not use the underlying reader at the same time.
//
// Use newMsgReader to wrap a message, then use set to extract payload data
// into fields and parameters.
type MsgReader struct {
	r   *bufio.Reader
	cur parameter
}

func NewMsgReader(m Message) *MsgReader {
	mr := &MsgReader{
		r: bufio.NewReader(m.payload),
	}
	return mr
}

func (mr *MsgReader) Reset(m Message) {
	mr.r.Reset(m.payload)
	mr.cur = parameter{}
}

type pT interface {
	getType() ParamType
}

type paramDecoder interface {
	decode(*MsgReader) error
}

// hasData returns true if the reader current parameter has data.
func (mr MsgReader) hasData() bool {
	return mr.cur.typ != paramInvalid && mr.cur.length > 0
}

// readParam reads the next parameter header, returning was the current one.
// Callers should use finishParam when done to replace the parameter context.
func (mr *MsgReader) readParam() (prev parameter, err error) {
	t, err := mr.readUint8()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			err = errors.Wrap(err, "failed to read parameter")
		}
		return prev, errors.Wrap(err, "no more data")
	}

	var p parameter
	if t&0x80 != 0 { // TV parameter
		p.typ = ParamType(t & 0x7F)
		l, ok := tvLengths[p.typ]
		if !ok {
			return prev, errors.Errorf("unknown TV type %v", p)
		}
		p.length = uint16(l)
	} else {
		p.typ = ParamType(uint16(t) << 8)
		t, err = mr.readUint8()
		if err != nil {
			return prev, errors.Wrap(err, "failed to read parameter")
		}
		p.typ |= ParamType(t)

		p.length, err = mr.readUint16()
		if err != nil {
			return prev, errors.Wrap(err, "failed to read parameter")
		}

		if p.length < tlvHeaderSz {
			return prev, errors.Errorf("invalid TLV header length: %v", p)
		}
		p.length -= tlvHeaderSz
	}

	if mr.cur.typ != paramInvalid {
		if p.length > mr.cur.length {
			return prev, errors.Errorf(
				"sub-parameter %v needs more bytes than those remaining in %v",
				p, mr.cur)
		}

		// "Remove" the sub-param from the current parameter.
		mr.cur.length -= p.length
	}

	prev = mr.cur
	mr.cur = p
	return prev, nil
}

// discard the current parameter by popping it off the stack
// and advancing the reader past any of its unread data.
func (mr *MsgReader) discard() error {
	if mr.cur.typ == 0 {
		return errors.New("no current parameter")
	}

	if mr.hasData() {
		_, err := mr.r.Discard(int(mr.cur.length))
		if err != nil {
			return errors.Wrapf(err, "failed to discard parameter %v", mr.cur)
		}
	}

	return nil
}

// ReadFields reads a series of required fields in a known order.
func (mr *MsgReader) ReadFields(fields ...interface{}) error {
	for i := range fields {
		if err := mr.read(fields[i]); err != nil {
			return err
		}
	}
	return nil
}

// ReadParameters reads a series of required parameters in a known order.
func (mr *MsgReader) ReadParameters(params ...pT) error {
	for i := range params {
		if err := mr.readParameter(params[i]); err != nil {
			return err
		}
	}
	return nil
}

func (mr *MsgReader) readOneOf(params ...pT) error {
	prev, err := mr.readParam()
	if err != nil {
		return err
	}

	for _, p := range params {
		if mr.cur.typ != p.getType() {
			continue
		}

		if err := mr.read(p); err != nil {
			return err
		}

		return mr.endParam(prev)
	}

	return errors.Errorf("unexpected parameter %v when looking for %v",
		mr.cur.typ, params)
}

// readParameter reads a single, required parameter,
// reading its header, decoding its data,
// and popping it off the stack once done.
func (mr *MsgReader) readParameter(p pT) error {
	prev, err := mr.readParam()
	if err != nil {
		return err
	}

	if mr.cur.typ != p.getType() {
		return errors.Errorf("unexpected parameter %v when looking for %v",
			mr.cur.typ, p.getType())
	}

	if err := mr.read(p); err != nil {
		return err
	}

	return mr.endParam(prev)
}

func (mr *MsgReader) endParam(prev parameter) error {
	if mr.hasData() {
		return errors.Errorf("parameter has unexpected data: %v", mr.cur)
	}

	mr.cur = prev
	return nil
}

// read a single field f.
//
// If f implements paramDecoder, this calls its decode method.
// Otherwise, f must one of the following types:
// -- *string: data is 2 byte length, followed by UTF-8 string data.
// -- *uint64: data is 8 byte unsigned int.
// -- *uint32: data is 4 byte unsigned int.
// -- *uint16: data is 2 byte unsigned int.
// --  *uint8: data is 1 byte unsigned int.
func (mr *MsgReader) read(f interface{}) error {
	if f == nil {
		return nil
	}

	var err error
	switch v := f.(type) {
	case paramDecoder:
		return v.decode(mr)
	case *string:
		*v, err = mr.readString()
	case *uint64:
		*v, err = mr.readUint64()
	case *uint32:
		*v, err = mr.readUint32()
	case *uint16:
		*v, err = mr.readUint16()
	case *uint8:
		*v, err = mr.readUint8()
	default:
		if p, ok := v.(pT); ok {
			err = errors.Errorf("unhandled type: %T for %v -- "+
				"should it implement paramDecoder or be cast?",
				v, p.getType())
		} else {
			err = errors.Errorf("unhandled type: %T -- "+
				"should it implement paramDecoder or be cast?", v)
		}
	}
	return err
}

// readString reads a UTF-8 encoded string from the current parameter,
// assuming its prepended by a uint16 indicating its length in bytes.
func (mr *MsgReader) readString() (string, error) {
	sLen, err := mr.readUint16()
	if err != nil {
		return "", errors.WithMessagef(err, "failed to read string length")
	}

	if sLen == 0 {
		return "", nil
	}

	if mr.cur.length < sLen {
		return "", errors.Errorf("not enough data to read string of length %d from %v",
			sLen, mr.cur)
	}
	mr.cur.length -= sLen

	sBuild := strings.Builder{}
	if _, err = io.CopyN(&sBuild, mr.r, int64(sLen)); err != nil {
		return "", errors.Wrapf(err, "failed to read string of length %d from %v",
			sLen, mr.cur)
	}

	return sBuild.String(), nil
}

// readUint64 reads an 8-bit uint from the payload.
func (mr *MsgReader) readUint8() (uint8, error) {
	u, err := mr.readUint(1)
	return uint8(u), err
}

// readUint64 reads a 16-bit uint from the payload.
func (mr *MsgReader) readUint16() (uint16, error) {
	u, err := mr.readUint(2)
	return uint16(u), err
}

// readUint64 reads a 32-bit uint from the payload.
func (mr *MsgReader) readUint32() (uint32, error) {
	u, err := mr.readUint(4)
	return uint32(u), err
}

// readUint64 reads a 64-bit uint from the payload.
func (mr *MsgReader) readUint64() (uint64, error) {
	return mr.readUint(8)
}

// readUint of size 0 < nBytes <= 8, (presumably 1, 2, 4, 8), assumed BigEndian.
// If nBytes is outside of that range, this will panic.
// This advances the reader by nBytes
// and subtracts them from the current parameter's length.
func (mr *MsgReader) readUint(nBytes int) (uint64, error) {
	if mr.cur.typ != paramInvalid && int(mr.cur.length) < nBytes {
		return 0, errors.Errorf("not enough data to read uint%d from %v", nBytes*8, mr.cur)
	}
	mr.cur.length -= uint16(nBytes)

	var u uint64
	for i := 0; i < nBytes; i++ {
		b, err := mr.r.ReadByte()
		if err != nil {
			return 0, errors.Wrapf(err, "failed to read uint%d", nBytes*8)
		}
		u = (u << 8) | uint64(b)
	}

	return u, nil
}
