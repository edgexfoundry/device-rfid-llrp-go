//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bytes"
	"github.com/pkg/errors"
)

// MsgBuilder provides a structured way to build LLRP messages.
//
// Create a new builder, write message fields/parameters, then use finish.
// The resulting message's payload is backed by the builder,
// so it shouldn't be modified until after the message is sent.
//
// Once sent, the builder can be reset to avoid unnecessary allocations.
type MsgBuilder struct {
	w *bytes.Buffer
}

// NewMsgBuilder returns a new MsgBuilder ready for use.
func NewMsgBuilder() *MsgBuilder {
	w := bytes.Buffer{}
	return &MsgBuilder{w: &w}
}

// Reset the builder so it can be reused.
func (mb *MsgBuilder) Reset() {
	mb.w.Reset()
}

// Finish building the message.
//
// The resulting message carries the given message type,
// but should be assigned a version and ID when sent.
//
// The MsgBuilder should not be reused until after the message is sent.
// The builder can be reset via Reset.
func (mb *MsgBuilder) Finish(mt MessageType) (Message, error) {
	if mb.w.Len() > int(maxPayloadSz) {
		return Message{}, errors.Errorf("payload size exceeds max: %d", mb.w.Len())
	}

	return Message{
		payload: mb.w,
		header: header{
			payloadLen: uint32(mb.w.Len()),
			typ:        mt,
		},
	}, nil
}

func (vn VersionNum) encode(mb *MsgBuilder) error {
	return mb.write(uint8(vn))
}

// WriteFields consecutively to the message.
// Fields must be either a uint type, string, or implement fieldEncoder.
func (mb *MsgBuilder) WriteFields(fields ...interface{}) error {
	for i := range fields {
		if err := mb.write(fields[i]); err != nil {
			return err
		}
	}
	return nil
}

// WriteParams consecutively to the message,
// prefixed with their header information as necessary.
func (mb *MsgBuilder) WriteParams(params ...pT) error {
	for i := range params {
		if err := mb.writeParam(params[i]); err != nil {
			return err
		}
	}
	return nil
}

// writeParam to the message with its header and length, if applicable.
func (mb *MsgBuilder) writeParam(p pT) error {
	pc, err := mb.startParam(p.getType())
	if err != nil {
		return err
	}
	if err := mb.write(p); err != nil {
		return err
	}
	return mb.finishParam(pc)
}

type fieldEncoder interface {
	encode(mb *MsgBuilder) error
}

// write a single field f.
//
// If f implements fieldEncoder, this calls its encode method.
// Otherwise, f must be one of the following types:
// -- string: written as a 2 byte length, followed its bytes (presumed to be UTF-8).
// -- uint64: written as 8 bytes
// -- uint32: written as 4 bytes
// -- uint16: written as 2 bytes
// --  uint8: written as 1 byte
func (mb *MsgBuilder) write(f interface{}) error {
	switch v := f.(type) {
	case fieldEncoder:
		return v.encode(mb)
	case uint64:
		return mb.writeUint(v, 8)
	case uint32:
		return mb.writeUint(uint64(v), 4)
	case uint16:
		return mb.writeUint(uint64(v), 2)
	case uint8:
		return mb.writeUint(uint64(v), 1)
	case string:
		return mb.writeString(v)
	default:
		if p, ok := v.(pT); ok {
			return errors.Errorf("unhandled type: %T for %v -- should it implement paramEncoder?",
				v, p.getType())
		}
		return errors.Errorf("unhandled type: %T -- should it implement paramEncoder?", v)
	}
}

// writeUint u using nBytes, which should be 1, 2, 4, or 8,
// but that isn't validated by this method.
func (mb *MsgBuilder) writeUint(v uint64, nBytes int) error {
	switch nBytes {
	case 8:
		mb.w.WriteByte(uint8(v >> 56))
		mb.w.WriteByte(uint8(v >> 48))
		mb.w.WriteByte(uint8(v >> 40))
		mb.w.WriteByte(uint8(v >> 32))
		fallthrough
	case 4:
		mb.w.WriteByte(uint8(v >> 24))
		mb.w.WriteByte(uint8(v >> 16))
		fallthrough
	case 2:
		mb.w.WriteByte(uint8(v >> 8))
		fallthrough
	case 1:
		mb.w.WriteByte(uint8(v))
	default:
		return errors.Errorf("not a valid uint size: %d", nBytes)
	}

	return nil
}

// writeString s to the stream by prepending its length as a uint16,
// followed by its data.
// LLRP string should be UTF-8 encoded, but this doesn't validate that.
func (mb *MsgBuilder) writeString(s string) error {
	ls := len(s)

	// make sure we're not dealing with a string with more than 2^16-2 bytes
	if ls > int(maxParamSz)-2 {
		return errors.Errorf("string has %d bytes and is larger than an LLRP parameter", len(s))
	}

	mb.w.WriteByte(byte(ls >> 8))
	mb.w.WriteByte(byte(ls & 0xFF))
	mb.w.WriteString(s)
	return nil
}

type paramCtx struct {
	start int
	parameter
}

// startParam starts a parameter context which must be finished with finishParam.
//
// The writes the parameter type to the stream (1 byte for TVs, 2 for TLVs),
// and for TLVs, reserves 2 bytes for the length,
// which must be set later via finishParam.
func (mb *MsgBuilder) startParam(pt ParamType) (paramCtx, error) {
	// log.Printf("starting %v", pt)
	pc := paramCtx{
		parameter: parameter{typ: pt},
		start:     mb.w.Len(),
	}

	if pt.isTV() {
		if _, ok := tvLengths[pt]; !ok {
			return pc, errors.Errorf("unknown TV %v", pt)
		}

		mb.w.WriteByte(byte(pt) | 0x80)
	} else {
		mb.w.WriteByte(uint8(pt >> 8 & 0b11))
		mb.w.WriteByte(uint8(pt))
		// length, to be set later
		mb.w.WriteByte(0x0)
		mb.w.WriteByte(0x0)
	}

	return pc, nil
}

// finishParam validates a parameter's length and pops it off the stack.
//
// For TLVs, it writes the the parameter's length into the header.
// For TVs, it validates that the written data matches the expected length.
// If this is a sub-parameter, it adds its length to its parent
// and validates that it fits within that parameter.
func (mb *MsgBuilder) finishParam(pc paramCtx) error {
	end := mb.w.Len()
	length := end - pc.start
	// log.Printf("finishing %v [%d to %d] (%d bytes)", pc.typ, pc.start, end, length)

	b := mb.w.Bytes()
	b[pc.start+3] = uint8(length & 0xFF)
	b[pc.start+2] = uint8(length >> 8)

	return nil
}
