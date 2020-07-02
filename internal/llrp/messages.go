//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

//go:generate stringer -type=VersionNum,MessageType

package llrp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
)

// MessageType corresponds to the LLRP binary encoding for message headers.
type MessageType uint16

const (
	GetReaderCapabilities         = MessageType(1)
	GetReaderConfig               = MessageType(2)
	SetReaderConfig               = MessageType(3)
	CloseConnectionResponse       = MessageType(4)
	GetReaderCapabilitiesResponse = MessageType(11)
	GetReaderConfigResponse       = MessageType(12)
	SetReaderConfigResponse       = MessageType(13)
	CloseConnection               = MessageType(14)
	AddROSpec                     = MessageType(20)
	DeleteROSpec                  = MessageType(21)
	StartROSpec                   = MessageType(22)
	StopROSpec                    = MessageType(23)
	EnableROSpec                  = MessageType(24)
	DisableROSpec                 = MessageType(25)
	GetROSpecs                    = MessageType(26)
	AddROSpecResponse             = MessageType(30)
	DeleteROSpecResponse          = MessageType(31)
	StartROSpecResponse           = MessageType(32)
	StopROSpecResponse            = MessageType(33)
	EnableROSpecResponse          = MessageType(34)
	DisableROSpecResponse         = MessageType(35)
	GetROSpecsResponse            = MessageType(36)
	AddAccessSpec                 = MessageType(40)
	DeleteAccessSpec              = MessageType(41)
	EnableAccessSpec              = MessageType(42)
	DisableAccessSpec             = MessageType(43)
	GetAccessSpecs                = MessageType(44)
	ClientRequestOp               = MessageType(45)
	GetSupportedVersion           = MessageType(46)
	SetProtocolVersion            = MessageType(47)
	AddAccessSpecResponse         = MessageType(50)
	DeleteAccessSpecResponse      = MessageType(51)
	EnableAccessSpecResponse      = MessageType(52)
	DisableAccessSpecResponse     = MessageType(53)
	GetAccessSpecsResponse        = MessageType(54)
	ClientRequestOpResponse       = MessageType(55)
	GetSupportedVersionResponse   = MessageType(56)
	SetProtocolVersionResponse    = MessageType(57)
	GetReport                     = MessageType(60)
	ROAccessReport                = MessageType(61)
	KeepAlive                     = MessageType(62)
	ReaderEventNotification       = MessageType(63)
	EnableEventsAndReports        = MessageType(64)
	KeepAliveAck                  = MessageType(72)
	ErrorMessage                  = MessageType(100)
	CustomMessage                 = MessageType(1023)

	minMsgType     = GetReaderCapabilities
	maxMsgType     = CustomMessage    // highest legal message type
	msgResvStart   = MessageType(900) // 900-999 are reserved for ISO/IEC 24971-5
	msgResvEnd     = MessageType(999)
	msgTypeInvalid = MessageType(0)

	headerSz     = 10                           // LLRP message headers are 10 bytes
	maxPayloadSz = uint32(1<<32 - 1 - headerSz) // max size for a payload
)

// responseType maps certain message types to their response type.
var responseType = map[MessageType]MessageType{
	GetSupportedVersion:   GetSupportedVersionResponse,
	SetProtocolVersion:    SetProtocolVersionResponse,
	GetReaderCapabilities: GetReaderCapabilitiesResponse,
	SetReaderConfig:       SetReaderConfigResponse,
	CloseConnection:       CloseConnectionResponse,
	CustomMessage:         CustomMessage,
}

// isValid returns true if the messageType is within the permitted messageType space.
func (mt MessageType) isValid() bool {
	return minMsgType <= mt && mt <= maxMsgType && !(msgResvStart <= mt && mt <= msgResvEnd)
}

// responseType returns the messageType of a response to a request of this type,
// or the zero value and false if there is not a known response type.
func (mt MessageType) responseType() (MessageType, bool) {
	t, ok := responseType[mt]
	return t, ok
}

type messageID uint32

type awaitMap = map[messageID]chan<- Message

// header holds information about an LLRP message header.
//
// Importantly, payloadLen does not include the header's 10 bytes;
// when a message is read, it's automatically subtracted,
// and when a message is written, it's automatically added.
// See header.UnmarshalBinary and header.MarshalBinary for more information.
type header struct {
	payloadLen uint32      // length of payload; 0 if message is header-only
	id         messageID   // for correlating request/response (uint32)
	typ        MessageType // message type: 10 bits (uint16)
	version    VersionNum  // version: 3 bits (uint8)
}

func (h header) String() string {
	return fmt.Sprintf("version: %v, id: %d (%#08[2]x), payloadLen: %d, type: %s (%[4]d, %#04x)",
		h.version, h.id, h.payloadLen, h.typ, uint16(h.typ))
}

func (m Message) String() string {
	return fmt.Sprintf("message{%v}", m.header)
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
		version:    VersionNum((buf[0] >> 2) & 0b111),
		typ:        MessageType(binary.BigEndian.Uint16(buf[0:2]) & (0b0011_1111_1111)),
		payloadLen: binary.BigEndian.Uint32(buf[2:6]),
		id:         messageID(binary.BigEndian.Uint32(buf[6:10])),
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
func validateHeader(payloadLen uint32, typ MessageType) error {
	if typ > maxMsgType {
		return msgErr("typ exceeds max message type: %d > %d", typ, maxMsgType)
	}
	if msgResvStart <= typ && typ <= msgResvEnd {
		return msgErr("message type %d is reserved", typ)
	}

	if payloadLen > maxPayloadSz {
		return msgErr(
			"payload length is larger than the max LLRP message size: %d > %d",
			payloadLen, maxPayloadSz)
	}

	return nil
}

// Message represents an LLRP message.
//
// For incoming messages,
// payload is guaranteed not to be nil,
// though if the payload length is zero,
// it will immediately return EOF.
// Incoming messages can be closed to discard its payload.
//
// For outgoing messages,
// payload may be nil to signal no data.
type Message struct {
	payload io.Reader
	header
}

// Close the message by discarding any remaining payload.
// This returns an error if discarding fails.
// It's safe to call this multiple times.
func (m Message) Close() error {
	if m.payload == nil {
		return nil
	}

	_, err := io.Copy(ioutil.Discard, m.payload)
	if err != nil {
		return errors.Wrap(err, "failed to discard payload")
	}

	if c, ok := m.payload.(io.Closer); ok && c != nil {
		return errors.Wrap(c.Close(), "message discarded, but close failed")
	}

	return nil
}

// isResponseTo returns nil if reqType's expected response type matches m's type.
// If it doesn't it returns an error with information about the mismatch.
func (m Message) isResponseTo(reqType MessageType) error {
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
// For now, this is intentionally not exported.
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
//
// Messages generated here need their version and mid set
// to match those relevant to the Reader connection before sending.
func newMessage(data io.Reader, payloadLen uint32, typ MessageType) Message {
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

	return Message{
		payload: payload,
		header: header{
			payloadLen: payloadLen,
			typ:        typ,
			version:    versionMin,
		},
	}
}

// NewHdrOnlyMsg prepares a message that has no payload.
func NewHdrOnlyMsg(typ MessageType) Message {
	return newMessage(nil, 0, typ)
}

// NewByteMessage uses a []byte payload to create a message.
// The caller should not modify the slice until the message is sent.
func NewByteMessage(typ MessageType, payload []byte) (m Message, err error) {
	// check this here, since len(data) could overflow a uint32
	if int64(len(payload)) > int64(maxPayloadSz) {
		return Message{}, errors.New("LLRP messages are limited to 4GiB (minus a 10 byte header)")
	}
	n := uint32(len(payload))
	return newMessage(bytes.NewReader(payload), n, typ), nil
}

// msgErr returns a new error for LLRP message issues.
func msgErr(why string, v ...interface{}) error {
	return errors.Errorf("invalid LLRP message: "+why, v...)
}
