//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

// Reader represents a connection to an LLRP-compatible RFID reader.
type Reader struct {
	conn      net.Conn       // underlying network connection
	done      chan struct{}  // closed when the Reader is closed
	isClosed  uint32         // used atomically to prevent duplicate closure of done
	sendQueue chan request   // controls write-side of connection
	ackQueue  chan messageID // gesundheit
	awaitMu   sync.Mutex     // synchronize awaiting map access
	awaiting  awaitMap       // message IDs -> awaiting reply
	logger    ReaderLogger   // reports pressure on the ACK queue

	handlerMu sync.RWMutex
	handlers  map[messageType]responseHandler

	version uint8 // sent in headers; established during negotiation
}

type messageID uint32
type messageType uint16
type awaitMap = map[messageID]chan<- response
type responseHandler interface {
	handle(r *response)
}

// NewReader returns a Reader configured by the given options.
func NewReader(opts ...ReaderOpt) (*Reader, error) {
	// todo: allow connection timeout parameters;
	//   call SetReadDeadline/SetWriteDeadline as needed

	r := &Reader{
		version:   1,
		done:      make(chan struct{}),
		sendQueue: make(chan request),
		ackQueue:  make(chan messageID, 5),
		awaiting:  make(awaitMap),
		handlers:  make(map[messageType]responseHandler),
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
func (r *Reader) Connect() error {
	defer r.conn.Close()
	defer r.Close()

	if err := r.initiate(); err != nil {
		return err
	}

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

// Shutdown attempts to gracefully close the connection.
func (r *Reader) Shutdown(ctx context.Context) error {
	_, err := r.SendMessage(ctx, nil, CloseConnection)
	if err != nil {
		return err
	}
	// todo
	return nil
}

// Close closes the Reader.
//
// After closing, the Reader can no longer serve connections.
func (r *Reader) Close() error {
	if atomic.CompareAndSwapUint32(&r.isClosed, 0, 1) {
		close(r.done)
		return nil
	} else {
		return ErrReaderClosed
	}
}

const maxBufferedPayloadSz = uint32((1 << 10) * 640)

// SendMessage sends the given data, assuming it matches the type.
// It returns the response data or an error.
func (r *Reader) SendMessage(ctx context.Context, data []byte, typ messageType) ([]byte, error) {
	var mOut msgOut
	if data == nil {
		mOut = newHdrOnlyMsg(typ)
	} else {
		var err error
		mOut, err = newByteMessage(data, typ)
		if err != nil {
			return nil, err
		}
	}

	resp, err := r.send(ctx, mOut)
	if err != nil {
		return nil, err
	}
	defer resp.payload.Close()

	if resp.hdr.typ == LLRPErrorMessage {
		/* todo
		if err := resp.parseErr(); err != nil {
			return nil, errors.WithMessage(err,
				"received ErrorMessage, but unable to parse it")
		}
		*/
	}

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
	maxMsgType   = messageType(1<<10 - 1)       // highest legal message type

	// Known Message Types
	GetSupportedVersion           = messageType(46)
	GetSupportedVersionResponse   = messageType(56)
	SetProtocolVersion            = messageType(47)
	SetProtocolVersionResponse    = messageType(57)
	GetReaderCapabilities         = messageType(1)
	GetReaderCapabilitiesResponse = messageType(11)
	KeepAlive                     = messageType(62)
	KeepAliveAck                  = messageType(72)
	ReaderEventNotification       = messageType(63)
	SetReaderConfig               = messageType(2)
	SetReaderConfigResponse       = messageType(13)
	CloseConnection               = messageType(14)
	CloseConnectionResponse       = messageType(4)
	CustomMessage                 = messageType(1023)
	LLRPErrorMessage              = messageType(100)
)

// responseType maps certain message types to their response type.
var responseType = map[messageType]messageType{
	GetSupportedVersion:   GetSupportedVersionResponse,
	SetProtocolVersion:    SetProtocolVersionResponse,
	GetReaderCapabilities: GetReaderCapabilitiesResponse,
	SetReaderConfig:       SetReaderConfigResponse,
	CloseConnection:       CloseConnectionResponse,
}

func (mt messageType) isValid() bool {
	return mt <= maxMsgType
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
	version    uint8       // version: 3 bits
}

// writeHeader writes a message header to the connection.
//
// It does not validate the parameters,
// as it assumes its already been done.
//
// Once a message header is written,
// length bytes must also be written to the stream,
// or the connection will be in an invalid state and should be closed.
func (r *Reader) writeHeader(mid messageID, payloadLen uint32, typ messageType) error {
	header := make([]byte, headerSz)
	binary.BigEndian.PutUint32(header[6:10], uint32(mid))
	binary.BigEndian.PutUint32(header[2:6], payloadLen+headerSz)
	binary.BigEndian.PutUint16(header[0:2], uint16(r.version)<<10|uint16(typ))
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
	err = errors.Wrap(err, "read header failed")
	return
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
		id:         messageID(binary.BigEndian.Uint32(buf[6:10])),
		payloadLen: binary.BigEndian.Uint32(buf[2:6]),
		typ:        messageType(binary.BigEndian.Uint16(buf[0:2]) & (0b0011_1111_1111)),
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
	binary.BigEndian.PutUint32(header[6:10], uint32(h.id))
	binary.BigEndian.PutUint32(header[2:6], h.payloadLen+headerSz)
	binary.BigEndian.PutUint16(header[0:2], uint16(h.version)<<10|uint16(h.typ))
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

		// Handle the payload.
		incoming := io.LimitReader(r.conn, int64(m.payloadLen))
		handler := r.getResponseHandler(m)
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
	var nextMsgID messageID

	for {
		// Get the next message to send, giving priority to ACKs.
		var mid messageID
		var msg msgOut

		select {
		case <-r.done:
			return ErrReaderClosed
		case mid = <-r.ackQueue:
		default:
			select {
			case <-r.done:
				return ErrReaderClosed
			case mid = <-r.ackQueue:
				msg = msgOut{typ: KeepAliveAck}
			case req := <-r.sendQueue:
				msg = req.msg

				// Generate the message ID.
				mid = nextMsgID
				nextMsgID++

				// Give the read-side a way to correlate the response
				// with something the sender can listen to.
				replyChan := make(chan response, 1)
				r.awaitMu.Lock()
				r.awaiting[mid] = replyChan
				r.awaitMu.Unlock()

				// Give the sender a way to clean up
				// in case a response never comes.
				token := sendToken{
					replyChan: replyChan,
					cancel: func() {
						r.awaitMu.Lock()
						if c, ok := r.awaiting[mid]; ok {
							close(c)
							delete(r.awaiting, mid)
						}
						r.awaitMu.Unlock()
					},
				}

				// Give those to the sender.
				req.tokenChan <- token
				close(req.tokenChan)
			}
		}

		if err := r.writeHeader(mid, msg.length, msg.typ); err != nil {
			return err
		}

		if msg.length == 0 {
			continue
		}

		if msg.data == nil {
			return errors.Errorf("message data is nil, but has length >0 (%d)",
				msg.length)
		}

		if n, err := io.Copy(r.conn, msg.data); err != nil {
			return errors.Wrapf(err, "write failed after %d bytes for "+
				"mid %d, type %d, length %d", n, mid, msg.typ, msg.length)
		}
	}
}

// sendAck acknowledges a KeepAlive message with the given message ID.
//
// If the Reader's connection is hung writing for some reason,
// it's possible the acknowledgement queue fills up,
// in which case it drops the ACK and logs the issue.
func (r *Reader) sendAck(mid messageID) {
	select {
	case r.ackQueue <- mid:
		r.logger.Println("Sending ACK")
	default:
		r.logger.Println("Discarding KeepAliveAck as queue is full. " +
			"This may indicate the Reader's write side is broken " +
			"yet not timing out.")
	}
}

// getResponseHandler returns the handler for a given message's response.
func (r *Reader) getResponseHandler(m header) io.Writer {
	// todo: handle LLRP error message type
	if m.typ == KeepAlive {
		r.sendAck(m.id)
		return ioutil.Discard
	}

	r.awaitMu.Lock()
	replyChan, ok := r.awaiting[m.id]
	delete(r.awaiting, m.id)
	r.awaitMu.Unlock()

	if !ok {
		return ioutil.Discard
	}

	pr, pw := io.Pipe()
	resp := response{hdr: m, payload: pr}
	replyChan <- resp
	close(replyChan)
	return pw
}

// msgOut represents an outgoing message.
type msgOut struct {
	data   io.Reader   // nil if no payload, in which case length MUST be 0.
	length uint32      // does not include header size
	typ    messageType // LLRP msg type
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
// If writing the data may fail or take a long time,
// the caller's should buffer the reader.
func newMessage(data io.Reader, payloadLen uint32, typ messageType) msgOut {
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
func newHdrOnlyMsg(typ messageType) msgOut {
	return newMessage(nil, 0, typ)
}

// newByteMessage uses a payload to create a msgOut.
// The caller should not modify the slice until the message is sent.
func newByteMessage(payload []byte, typ messageType) (m msgOut, err error) {
	// check this here, since len(data) could overflow a uint32
	if int64(len(payload)) > int64(maxPayloadSz) {
		return msgOut{}, errors.New("LLRP messages are limited to 4GiB (minus a 10 byte header)")
	}
	n := uint32(len(payload))
	return newMessage(bytes.NewReader(payload), n, typ), nil
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

// typesMatch returns an error if a response's type does not match
// the expected type associated with a given request type.
func (resp response) typesMatch(reqType messageType) error {
	expectedRespType, ok := reqType.responseType()
	if !ok {
		return errors.Errorf("unknown request type %d", reqType)
	}

	if resp.hdr.typ != expectedRespType {
		return errors.Errorf("response message type (%d) "+
			"does not match request's expected response type (%d -> %d)",
			resp.hdr.typ, reqType, expectedRespType)
	}
	return nil
}

// request is sent to the write coordinator to start a new request.
// A sender puts a request in the sendQueue with a valid tokenChan.
// When the write coordinator processes the request,
// it'll use the channel to send back response/cancellation data.
type request struct {
	msg       msgOut           // the message to send
	tokenChan chan<- sendToken // closed by the write coordinator
}

// sendToken is sent back to the sender in response to a send request.
type sendToken struct {
	replyChan <-chan response // closed by the read coordinator
	cancel    func()          // The sender should call this if it stops waiting for the reply.
}

// send a message as soon as possible and wait for its response.
//
// It is the caller's responsibility to read the data or close the response.
// If the caller is slow or may panic, it should buffer its data.
// Canceling the context will stop processing at the earliest safe point,
// but if you're already up for writing, you must complete your outgoing message;
// likewise, once you receive a response, you must read or close it.
//
// If you cancel the context successfully, you'll receive (nil, ctx.Err()).
// If the reader closes while you're waiting to send or awaiting the reply,
// you'll receive an error wrapping ErrReaderClosed.
//
// This method is meant primarily as an internal building block
// (see SendMessage for a more friendly wrapper).
// This method streams the outgoing and incoming data
// to avoid duplicating the memory of outgoing messages
// or processing unwanted responses, in full or in part.
// The sender gets exclusive access to part of the connection,
// so blocking, panics, or writing invalid data can break the connection
// or stop other senders from sending/receiving messages.
//
// If err is non-nil, the response payload is non-nil;
// however, if payloadLen is zero, it will return EOF immediately.
func (r *Reader) send(ctx context.Context, mOut msgOut) (*response, error) {
	select {
	default:
	case <-r.done:
		return nil, errors.Wrap(ErrReaderClosed, "failed to send")
	}

	// The write coordinator sends us a token to read or cancel the reply.
	// We shouldn't close this channel once the request is accepted.
	tokenChan := make(chan sendToken, 1)
	req := request{msg: mOut, tokenChan: tokenChan}

	// Wait until the message is sent, unless the Reader is closed.
	select {
	case <-r.done:
		close(tokenChan)
		return nil, errors.Wrap(ErrReaderClosed, "message not sent")
	case <-ctx.Done():
		close(tokenChan)
		return nil, ctx.Err()
	case r.sendQueue <- req:
		// The message was accepted; we can no longer cancel sending.
	}

	token := <-tokenChan // This should complete basically immediately.

	// Now we wait for the reply.
	select {
	case <-r.done:
		token.cancel()
		return nil, errors.Wrap(ErrReaderClosed, "message sent, but not awaited")
	case <-ctx.Done():
		token.cancel()
		return nil, ctx.Err()
	case resp := <-token.replyChan:
		return &resp, nil
	}
}

// initiate reads what it assumes to be the first message on a connection,
// confirms it's a ReaderEventNotification,
// and verifies its payload.
// It should be called before handling incoming other requests.
func (r *Reader) initiate() error {
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

	return nil
}

// negotiate performs version negotiation with a Reader.
// It assumes the connection has just started.
func (r *Reader) negotiate() error {
	// In version 1, there is no version negotiation.
	if r.version == 1 {
		return nil
	}

	// todo: version negotiation if r.version > 1

	resp, err := r.send(context.TODO(), newHdrOnlyMsg(GetSupportedVersion))
	if err != nil {
		return errors.WithMessage(err, "failed to Get Supported Versions")
	}
	defer resp.payload.Close()
	// todo: unmarshal message; check version
	if err := resp.typesMatch(GetSupportedVersion); err != nil {
		return errors.WithMessage(err, "failed to Get Supported Versions")
	}

	resp, err = r.send(context.TODO(), newHdrOnlyMsg(SetProtocolVersion))
	if err != nil {
		return errors.WithMessage(err, "failed to Set Protocol Version")
	}
	defer resp.payload.Close()
	// todo: unmarshal message; confirm version match or downgrade
	if err := resp.typesMatch(SetProtocolVersion); err != nil {
		return errors.WithMessage(err, "failed to Set Protocol Version")
	}

	return nil
}
