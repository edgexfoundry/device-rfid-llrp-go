//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"bytes"
	"encoding/binary"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"log"
	"net"
	"sync"
)

// Reader represents a connection to an LLRP-compatible RFID reader.
type Reader struct {
	conn          net.Conn      // underlying network connection
	done          chan struct{} // closed when the Reader is closed
	doneMu        sync.Mutex    // prevent duplicate calls to close
	sendQueue     chan request  // controls write-side of connection
	awaitingReply sync.Map      // message IDs -> requests
	ackQueue      chan uint32   // gesundheit
	logger        ReaderLogger  // reports pressure on the ACK queue

	// todo: async handlers for reports
	handlers map[uint16]func(header, io.Reader)

	version uint8 // sent in headers; established during negotiation
}

// NewReader returns a Reader configured by the given options.
func NewReader(opts ...ReaderOpt) (*Reader, error) {
	// todo: allow connection timeout parameters;
	//   call SetReadDeadline/SetWriteDeadline as needed

	r := &Reader{
		version:   1,
		done:      make(chan struct{}),
		sendQueue: make(chan request),
		ackQueue:  make(chan uint32, 5),
		// todo: handlers:  make(map[uint16]func(header, io.Reader)),
	}

	for _, opt := range opts {
		if err := opt.do(r); err != nil {
			return nil, err
		}
	}

	if r.conn == nil {
		return nil, errors.New("Reader has no connection")
	}

	// currently, only supporting version 1
	if r.version == 0 || r.version > 1 {
		return nil, errors.Errorf("unsupported version: %d", r.version)
	}

	if r.logger == nil {
		r.logger = stdGoLogger
	}

	return r, nil
}

// ReaderOpt modifies a Reader during construction.
type ReaderOpt interface {
	do(*Reader) error // don't allow arbitrary implementations for now
}

type readerOpt func(r *Reader) error

func (ro readerOpt) do(r *Reader) error {
	return ro(r)
}

// WithConn sets the Reader's connection.
func WithConn(conn net.Conn) ReaderOpt {
	return readerOpt(func(r *Reader) error {
		r.conn = conn
		return nil
	})
}

// WithVersion sets the expected LLRP version number.
// The actual version number used during communication is selected when connecting.
// Currently, only version 1 is supported; others will panic.
func WithVersion(v uint8) ReaderOpt {
	if v != 1 {
		panic("currently, only version 1 is supported")
	}
	return readerOpt(func(r *Reader) error {
		r.version = v
		return nil
	})
}

// WithLogger sets a logger for the Reader.
func WithLogger(l ReaderLogger) ReaderOpt {
	return readerOpt(func(r *Reader) error {
		r.logger = l
		return nil
	})
}

// ReaderLogger is used by the Reader to log certain status messages.
type ReaderLogger interface {
	Println(v ...interface{})
	Printf(fmt string, v ...interface{})
}

// stdGoLogger uses the standard Go logger to satisfy the ReaderLogger interface.
var stdGoLogger = &log.Logger{}

// ErrReaderClosed is returned if an operation is attempted on a closed Reader.
var ErrReaderClosed = errors.New("Reader closed")

// Connect to a Reader and start processing messages.
//
// This takes ownership of the connection,
// which it assumes is already dialed.
// It will serve the connection's incoming and outgoing messages
// until it encounters an error or the Reader is closed.
// Before returning, it closes the connection.
//
// If the Reader is closed, this returns ErrReaderClosed;
// otherwise, it returns the first error it encounters.
func (r *Reader) Connect(conn net.Conn) error {
	defer conn.Close()
	defer r.Close()

	errs := make(chan error, 2)
	go func() { errs <- r.handleOutgoing() }()
	go func() { errs <- r.handleIncoming() }()

	err := r.negotiate()
	if err != nil {
		return err
	}

	select {
	case err = <-errs:
	case <-r.done:
		err = ErrReaderClosed
	}

	return err
}

// Close closes the Reader.
//
// After closing, the Reader can no longer serve connections.
func (r *Reader) Close() error {
	var err error
	r.doneMu.Lock()
	if r.done != nil {
		close(r.done)
		r.done = nil
	} else {
		err = ErrReaderClosed
	}
	r.doneMu.Unlock()
	return err
}

const maxBufferedPayloadSz = uint32((1 << 10) * 640)

// SendMessage sends the given data, assuming it matches the type.
// It returns the response data or an error.
func (r *Reader) SendMessage(data []byte, typ uint16) ([]byte, error) {
	var mOut msgOut
	if data == nil {
		mOut = newHdrOnlyMsg(typ)
	} else {
		// check this here, since len(data) could overflow a uint32
		if int64(len(data)) > int64(maxPayloadSz) {
			return nil, errors.New("LLRP messages are limited to 4GiB (minus a 10 byte header)")
		}
		n := uint32(len(data))
		if err := validateHeader(n, typ); err != nil {
			return nil, err
		}

		mOut = newMessage(bytes.NewReader(data), n, typ)
	}

	resp, err := r.send(mOut)
	if err != nil {
		return nil, err
	}
	defer resp.payload.Close()

	if resp.hdr.payloadLen > maxBufferedPayloadSz {
		return nil, errors.Errorf("message payload exceeds max: %d > %d",
			resp.hdr.payloadLen, maxBufferedPayloadSz)
	}

	buffResponse := make([]byte, resp.hdr.payloadLen)
	if _, err := io.ReadFull(resp.payload, buffResponse); err != nil {
		return nil, err
	}

	return buffResponse, nil
}

const (
	headerSz     = 10                           // LLRP message headers are 10 bytes
	maxPayloadSz = uint32(1<<32 - 1 - headerSz) // max size for a payload
	maxMsgType   = uint16(1<<10 - 1)            // highest valid message type

	// Message types
	GetSupportedVersion           = 46
	GetSupportedVersionResponse   = 56
	SetProtocolVersion            = 47
	SetProtocolVersionResponse    = 57
	GetReaderCapabilities         = 1
	GetReaderCapabilitiesResponse = 11
	KeepAlive                     = 62
	KeepAliveAck                  = 72
	ReaderEventNotification       = 63
)

// responseType maps certain message types to their response type.
var responseType = map[uint16]uint16{
	GetSupportedVersion:   GetSupportedVersionResponse,
	SetProtocolVersion:    SetProtocolVersionResponse,
	GetReaderCapabilities: GetReaderCapabilitiesResponse,
}

// header holds information about an LLRP message header.
//
// Importantly, payloadLen does not include the header's 10 bytes;
// when a message is read, it's automatically subtracted,
// and when a message is written, it's automatically added.
// See header.UnmarshalBinary and header.MarshalBinary for more information.
type header struct {
	payloadLen uint32 // length of payload; 0 if message is header-only
	id         uint32 // message ID for correlating request/response
	typ        uint16 // message type: 10 bits
	version    uint8  // version: 3 bits
}

// writeHeader writes a message header to the connection.
//
// It does not validate the parameters,
// as it assumes its already been done.
//
// Once a message header is written,
// length bytes must also be written to the stream,
// or the connection will be in an invalid state and should be closed.
func (r *Reader) writeHeader(mid, payloadLen uint32, typ uint16) error {
	header := make([]byte, headerSz)
	binary.BigEndian.PutUint32(header[6:10], mid)
	binary.BigEndian.PutUint32(header[2:6], payloadLen+headerSz)
	binary.BigEndian.PutUint16(header[0:2], uint16(r.version)<<10|typ)
	_, err := r.conn.Write(header)
	return errors.Wrapf(err, "failed to write header")
}

// readHeader returns the next message header from the connection.
//
// It assumes that the next bytes on the incoming stream are a header,
// and that nothing else is attempting to read from the connection.
//
// This method blocks until reading the bytes,
// unless the underlying connection is closed,
// use net.Conn's SetReadDeadline to arrange for such.
//
// todo: call SetReadDeadline
func (r *Reader) readHeader() (mh header, err error) {
	buf := make([]byte, headerSz) // could this be a field of the Reader?
	if _, err = io.ReadFull(r.conn, buf); err == nil {
		err = mh.UnmarshalBinary(buf)
	}
	return
}

// validateHeader returns an error if the parameters aren't valid for an LLRP header.
func validateHeader(payloadLen uint32, typ uint16) error {
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

// UnmarshalBinary unmarshals the a header buffer into the message header.
//
// The payload length is the message length less the header size,
// unless the subtraction would overflow,
// in which case this returns an error indicating the impossible size.
func (h *header) UnmarshalBinary(buf []byte) error {
	if len(buf) < headerSz {
		return msgErr("not enough data for a message header: %d < %d", len(buf), headerSz)
	}

	*h = header{
		id:         binary.BigEndian.Uint32(buf[6:10]),
		payloadLen: binary.BigEndian.Uint32(buf[2:6]),
		typ:        binary.BigEndian.Uint16(buf[0:2]) & (0b0011_1111_1111),
		version:    buf[0] >> 2 & 0b111,
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
	binary.BigEndian.PutUint32(header[6:10], h.id)
	binary.BigEndian.PutUint32(header[2:6], h.payloadLen+headerSz)
	binary.BigEndian.PutUint16(header[0:2], uint16(h.version)<<10|h.typ)
	return header, nil
}

// handleIncoming handles the read side of the connection.
//
// If it encounters an error, it stops and returns it.
// Otherwise, it serves messages until the Reader is closed,
// at which point it and all future calls return ErrReaderClosed.
//
// Responses to requests are streamed to their sender.
// See send for information about sending messages/receiving responses.
//
// KeepAlive messages are acknowledged automatically as soon as possible.
//
// todo: handle asynchronous reports
// todo: SetReadDeadline
func (r *Reader) handleIncoming() error {
	for {
		select {
		case <-r.done:
			return ErrReaderClosed
		default:
		}

		// Wait for the next message.
		m, err := r.readHeader()
		if err != nil {
			return err
		}

		// Handle keep-alive messages directly.
		// todo: however we write handle async handlers,
		//  we can probably make this use the same idea.
		if m.typ == KeepAlive {
			if m.payloadLen != 0 {
				return errors.New("received keep alive with non-zero length")
			}

			r.sendAck(m.id)
			continue
		}

		// Handle the payload.
		incoming := io.LimitReader(r.conn, int64(m.payloadLen))
		handler := r.getHandler(m)
		_, err = io.Copy(handler, incoming)
		if pw, ok := handler.(*io.PipeWriter); ok {
			pw.Close()
		}

		switch err {
		case nil:
		case io.ErrClosedPipe:
			// The message handler stopped listening; that's OK,
			// but we still need to read the rest of the payload.
			if _, err := io.Copy(ioutil.Discard, incoming); err != nil {
				return errors.Wrap(err, "failed to discard payload")
			}
		case io.EOF:
			// The connection may have closed.
			return errors.Wrap(io.ErrUnexpectedEOF, "failed to read full payload")
		default:
			return errors.Wrap(err, "failed to process response")
		}
	}
}

// handleOutgoing manages the write side of the connection.
// See send for information about sending a message.
//
// If it encounters an error, it stops and returns it.
// Otherwise, it serves messages until the Reader is closed,
// at which point it and all future calls return ErrReaderClosed.
//
// todo: conn.SetWriteDeadline
func (r *Reader) handleOutgoing() error {
	var nextMsgID uint32

	for {
		// Get the next message to send, giving priority to ACKs.
		var mid uint32
		var req request
		select {
		case <-r.done:
			return ErrReaderClosed
		case mid = <-r.ackQueue:
		default:
			select {
			case <-r.done:
				return ErrReaderClosed
			case mid = <-r.ackQueue:
				req = request{msg: msgOut{typ: KeepAliveAck}}
			case req = <-r.sendQueue:
				// Generate the message ID for non-ACKs
				mid = nextMsgID
				nextMsgID++
				r.awaitingReply.Store(mid, req)
			}
		}

		if err := r.writeHeader(mid, req.msg.length, req.msg.typ); err != nil {
			return err
		}

		if req.msg.length == 0 {
			continue
		}

		if req.msg.data == nil {
			return errors.Errorf("message data is nil, but has length >0 (%d)",
				req.msg.length)
		}

		if _, err := io.Copy(r.conn, req.msg.data); err != nil {
			return errors.Wrapf(err, "failed to write message")
		}
	}
}

// sendAck acknowledges a KeepAlive message with the given message ID.
//
// If the Reader's connection is hung writing for some reason,
// it's possible the acknowledgement queue fills up,
// in which case it drops the ACK and logs the issue.
func (r *Reader) sendAck(mid uint32) {
	select {
	case r.ackQueue <- mid:
	default:
		r.logger.Println("Discarding KeepAliveAck as queue is full. " +
			"This may indicate the Reader's write side is broken " +
			"yet not timing out.")
	}
}

// getHandler returns the handler for a given message.
//
// If the payload length is non-zero,
// other read are blocked until the payload is read.
func (r *Reader) getHandler(m header) io.Writer {
	// todo: handle LLRP error message type

	ar, ok := r.awaitingReply.Load(m.id)
	if !ok || ar == nil {
		return ioutil.Discard
	}

	r.awaitingReply.Delete(m.id)
	req := ar.(request)
	var err error

	if rspTyp, ok := responseType[req.msg.typ]; ok {
		if m.typ != rspTyp {
			err = errors.Errorf("response message type (%d) "+
				"does not match request (%d -> %d)",
				m.typ, req.msg.typ, rspTyp)
		}
	}

	req.replyChan <- reply{
		response: response{hdr: m},
		err:      err,
	}

	close(req.replyChan)
	return req.pw
}

// msgOut represents an outgoing message.
type msgOut struct {
	data   io.Reader // nil if no payload, in which case length MUST be 0.
	length uint32    // does not include header size
	typ    uint16    // LLRP msg type
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
// the write must completely, otherwise the connection must be reset.
// If reading the data may fail or take a long time,
// the caller's should buffer the reader.
func newMessage(data io.Reader, payloadLen uint32, typ uint16) msgOut {
	if err := validateHeader(payloadLen, typ); err != nil {
		panic(err)
	}

	if data != nil {
		if payloadLen == 0 {
			panic("data is not nil, but length is 0")
		}
	} else if payloadLen != 0 {
		panic("length >0, but data is nil")
	}

	return msgOut{
		data:   data,
		length: payloadLen,
		typ:    typ,
	}
}

// newHdrOnlyMsg prepares a message that has no payload.
func newHdrOnlyMsg(typ uint16) msgOut {
	return newMessage(nil, 0, typ)
}

// msgErr returns a new error for LLRP message issues.
func msgErr(why string, v ...interface{}) error {
	return errors.Errorf("invalid LLRP message: "+why, v...)
}

// response is returned to a message sender.
//
// payload is guaranteed not to be nil,
// though if the payload length is zero,
// it will immediately return EOF.
//
// If the payload length is non-zero,
// the connection's read side blocks until the payload is read or closed.
// Message senders are responsible for closing the payload,
// though it is not necessary to read it entirely.
//
// If the Reader may be slow or can panic,
// the caller should read the message into a buffer
// and/or arrange for the payload to be closed.
type response struct {
	hdr     header        // the response's LLRP header
	payload io.ReadCloser // always non-nil; callers should read or close
}

// request is an internal struct to correlate responses to replies.
type request struct {
	replyChan chan<- reply   // return channel for the reply
	msg       msgOut         // the message to send
	pr        *io.PipeReader // where the handler can read the payload
	pw        *io.PipeWriter // where we'll write the reply
}

// reply is an internal struct sent from the receive loop to the send method.
type reply struct {
	response
	err error
}

// send a message as soon as possible and wait for its response.
//
// It is the caller's responsibility to read the data or close the response.
// If you receive an error, then the reader will be closed for you.
// Writes and reads are streamed over the connection,
// so once your message is up for writing,
// you have exclusive access to the write-side of the connection
// and must complete your write before others can continue.
// Likewise, if you get a non-nil response with a non-zero payload,
// you have exclusive access to the read-side of the connection
// and reading blocks until you read the data or close the Reader.
//
// If the caller is slow or may panic, it should buffer its data.
//
// The response payload is always non-nil,
// but if payloadLen is zero, it will return EOF immediately.
func (r *Reader) send(mOut msgOut) (*response, error) {
	if _, ok := <-r.done; ok {
		return nil, ErrReaderClosed
	}

	replyChan := make(chan reply, 1)
	pr, pw := io.Pipe()
	req := request{msg: mOut, replyChan: replyChan, pw: pw}

	// Wait until the message is sent, unless the Reader is closed.
	select {
	case <-r.done:
		close(replyChan)
		return nil, ErrReaderClosed
	case r.sendQueue <- req:
	}

	// The message was accepted by send queue.
	// Sending can no longer be canceled.
	// The receiver now owns the reply channel,
	// and we should not close it.
	// Now we wait for the reply.
	select {
	case <-r.done:
		// Don't close the reply channel; the receiver owns it now.
		_ = pr.Close() // close the pipe to prevent blocking the Reader
		return nil, ErrReaderClosed
	case rpl := <-replyChan:
		resp := &response{hdr: rpl.hdr, payload: pr}
		if rpl.err != nil {
			return resp, errors.Wrap(rpl.err, "failed to read response")
		}
		return resp, nil
	}
}

// negotiate performs version negotiation with a Reader.
// It assumes the connection has just started.
func (r *Reader) negotiate() error {
	connStatus, err := r.readHeader()
	if err != nil {
		return err
	}

	if connStatus.typ != ReaderEventNotification {
		return errors.WithMessagef(err, "message type %d != ReaderEventNotification",
			connStatus.typ)
	}
	if connStatus.payloadLen == 0 {
		return errors.WithMessagef(err,
			"ReaderEventNotification payload is empty")
	}

	// todo: unmarshal message; check for ConnectionSuccessful

	buf := make([]byte, connStatus.payloadLen)
	if _, err := io.ReadFull(r.conn, buf); err != nil {
		return errors.Wrap(err, "unable to read connection status")
	}

	// In version 1, there is no version negotiation.
	if r.version == 1 {
		return nil
	}

	// todo: version negotiation if r.version > 1

	resp, err := r.send(newHdrOnlyMsg(GetSupportedVersion))
	if err != nil {
		return errors.WithMessage(err, "failed to Get Supported Versions")
	}
	defer resp.payload.Close()
	// todo: unmarshal message; check version

	resp, err = r.send(newHdrOnlyMsg(SetProtocolVersion))
	if err != nil {
		return errors.WithMessage(err, "failed to Set Protocol Version")
	}
	defer resp.payload.Close()
	// todo: unmarshal message; confirm version match or downgrade

	return nil
}

// readParamHeader
// todo: process LLRP parameters
func (r *Reader) readParamHeader() (ph paramHeader, err error) {
	buf := make([]byte, 4)

	// TVs can be as short as a single byte. TLVs are at least 2.
	if _, err = r.conn.Read(buf[0:1]); err != nil {
		return
	}

	// The first bit in the stream of a TV is 1.
	// The next 7 bits are the type; length depends on type.
	if buf[0]&0b1000_0000 != 0 {

	}

	// TLVs have a 0 bit first. The next 5 bits must be zero.
	// The following 10 are the Type, then 16 for the length
	if _, err = r.conn.Read(buf[1:4]); err != nil {
		return
	}

	return
}

type paramHeader struct {
	typ    uint16 // 8 or 10 bits; TVs are 0-127; TLVs are 128-2047.
	length uint16 // only present for TLVs
}
