//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
)

const (
	// Known Message Types
	GetReaderCapabilities         = messageType(1)
	GetReaderConfig               = messageType(2)
	GetReaderConfigResponse       = messageType(12)
	SetReaderConfig               = messageType(3)
	CloseConnectionResponse       = messageType(4)
	GetReaderCapabilitiesResponse = messageType(11)
	SetReaderConfigResponse       = messageType(13)
	CloseConnection               = messageType(14)
	GetSupportedVersion           = messageType(46)
	SetProtocolVersion            = messageType(47)
	GetSupportedVersionResponse   = messageType(56)
	SetProtocolVersionResponse    = messageType(57)
	KeepAlive                     = messageType(62)
	ReaderEventNotification       = messageType(63)
	KeepAliveAck                  = messageType(72)
	ErrorMessage                  = messageType(100)
	CustomMessage                 = messageType(1023)

	minMsgType = GetReaderCapabilities
	maxMsgType = messageType(1<<10 - 1) // highest legal message type

	headerSz     = 10                           // LLRP message headers are 10 bytes
	maxPayloadSz = uint32(1<<32 - 1 - headerSz) // max size for a payload
)

// responseType maps certain message types to their response type.
var responseType = map[messageType]messageType{
	GetSupportedVersion:   GetSupportedVersionResponse,
	SetProtocolVersion:    SetProtocolVersionResponse,
	GetReaderCapabilities: GetReaderCapabilitiesResponse,
	SetReaderConfig:       SetReaderConfigResponse,
	CloseConnection:       CloseConnectionResponse,
}

// isValid returns true if the messageType is within the permitted messageType space.
func (mt messageType) isValid() bool {
	return mt >= minMsgType && mt <= maxMsgType
}

// responseType returns the messageType of a response to a request of this type,
// or the zero value and false if there is not a known response type.
func (mt messageType) responseType() (messageType, bool) {
	t, ok := responseType[mt]
	return t, ok
}

// header holds information about an LLRP message header.
//
// Importantly, payloadLen does not include the header's 10 bytes;
// when a message is read, it's automatically subtracted,
// and when a message is written, it's automatically added.
// See header.UnmarshalBinary and header.MarshalBinary for more information.
type header struct {
	payloadLen uint32      // length of payload; 0 if message is header-only
	id         messageID   // for correlating request/response
	typ        messageType // message type: 10 bits
	version    VersionNum  // version: 3 bits
}

func (h *header) String() string {
	return fmt.Sprintf("{id: %d (% 08x), type: %d (% 08x), payload len: %d bytes, version: %v}",
		h.id, h.id, h.typ, h.typ, h.payloadLen, h.version)
}

// UnmarshalBinary unmarshals the a header buffer into the message header.
//
// The payload length is the message length less the header size,
// unless the subtraction would overflow,
// in which case this returns an error indicating the impossible size.
func (h *header) UnmarshalBinary(buf []byte) error {
	if len(buf) < headerSz {
		return msgErr("not enough data for a message header: %d < %d", len(buf), headerSz)
	}

	_ = buf[9] // prevent extraneous bounds checks: golang.org/issue/14808
	*h = header{
		id:         messageID(binary.BigEndian.Uint32(buf[6:10])),
		payloadLen: binary.BigEndian.Uint32(buf[2:6]),
		typ:        messageType(binary.BigEndian.Uint16(buf[0:2]) & (0b0011_1111_1111)),
		version:    VersionNum(buf[0] >> 2 & 0b111),
	}

	if h.payloadLen < headerSz {
		return msgErr("message length is smaller than the minimum: %d < %d",
			h.payloadLen, headerSz)
	}
	h.payloadLen -= headerSz

	return nil
}

// MarshalBinary marshals a header to a byte array.
func (h *header) MarshalBinary() ([]byte, error) {
	if err := validateHeader(h.payloadLen, h.typ); err != nil {
		return nil, err
	}

	header := make([]byte, headerSz)
	binary.BigEndian.PutUint32(header[6:10], uint32(h.id))
	binary.BigEndian.PutUint32(header[2:6], h.payloadLen+headerSz)
	binary.BigEndian.PutUint16(header[0:2], uint16(h.version)<<10|uint16(h.typ))
	return header, nil
}

// validateHeader returns an error if the parameters aren't valid for an LLRP header.
func validateHeader(payloadLen uint32, typ messageType) error {
	if typ > maxMsgType {
		return msgErr("typ exceeds max message type")
	}

	if payloadLen > maxPayloadSz {
		return msgErr(
			"payload length is larger than the max LLRP message size: %d > %d",
			payloadLen, maxPayloadSz)
	}

	return nil
}

// message represents an LLRP message.
//
// For incoming messages,
// payload is guaranteed not to be nil,
// though if the payload length is zero,
// it will immediately return EOF.
//
// For outgoing messages,
// payload may be nil to signal no data.
type message struct {
	header
	payload io.ReadCloser
}

// isResponseTo returns nil if reqType's expected response type matches m's type.
// If it doesn't it returns an error with information about the mismatch.
func (m message) isResponseTo(reqType messageType) error {
	expectedRespType, ok := reqType.responseType()
	if !ok {
		return errors.Errorf("unknown request type %d", reqType)
	}

	if m.typ != expectedRespType {
		return errors.Errorf("response message type (%d) "+
			"does not match request's expected response type (%d -> %d)",
			m.typ, reqType, expectedRespType)
	}
	return nil
}

// newMessage prepares a message for sending.
//
// payloadLen should NOT include the header size,
// as it'll be added for you.
// If it is zero, data MUST be nil.
// Likewise, if data is nil, payloadLen MUST be zero.
// This method panics if these constraints are invalid.
//
// Calling this method does not block other operations,
// and it is safe for concurrent use.
//
// On the other hand, when the message is sent,
// exactly payloadLen bytes must be streamed from data,
// blocking other writers until the message completes.
// Because the message header is written before streaming data,
// the write must complete, otherwise the connection must be reset.
// If writing the data may fail or take a long time,
// the caller should buffer the message.
func newMessage(data io.Reader, payloadLen uint32, typ messageType) message {
	if err := validateHeader(payloadLen, typ); err != nil {
		panic(err)
	}

	var payload io.ReadCloser
	if data != nil {
		if payloadLen == 0 {
			panic("data is not nil, but length is 0")
		}
		var ok bool
		payload, ok = data.(io.ReadCloser)
		if !ok {
			payload = ioutil.NopCloser(data)
		}
	} else if payloadLen != 0 {
		panic("length >0, but data is nil")
	}

	return message{
		header: header{
			payloadLen: payloadLen,
			typ:        typ,
		},
		payload: payload,
	}
}

// newHdrOnlyMsg prepares a message that has no payload.
func newHdrOnlyMsg(typ messageType) message {
	return newMessage(nil, 0, typ)
}

// newByteMessage uses a payload to create a message.
// The caller should not modify the slice until the message is sent.
func newByteMessage(payload []byte, typ messageType) (m message, err error) {
	// check this here, since len(data) could overflow a uint32
	if int64(len(payload)) > int64(maxPayloadSz) {
		return message{}, errors.New("LLRP messages are limited to 4GiB (minus a 10 byte header)")
	}
	n := uint32(len(payload))
	return newMessage(ioutil.NopCloser(bytes.NewReader(payload)), n, typ), nil
}

// msgErr returns a new error for LLRP message issues.
func msgErr(why string, v ...interface{}) error {
	return errors.Errorf("invalid LLRP message: "+why, v...)
}
