//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"context"
	"encoding/binary"
	goErrs "errors"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
)

// Reader represents a connection to an LLRP-compatible RFID reader.
type Reader struct {
	conn      net.Conn       // underlying network connection
	done      chan struct{}  // closed when the Reader is closed
	ready     chan struct{}  // closed when the connection is negotiated
	isClosed  uint32         // used atomically to prevent duplicate closure of done
	sendQueue chan request   // controls write-side of connection
	ackQueue  chan messageID // gesundheit -- allows ACK'ing fast, unless sendQueue is unhealthy
	awaitMu   sync.Mutex     // synchronize awaiting map access
	awaiting  awaitMap       // message IDs -> awaiting reply
	logger    ReaderLogger   // reports pressure on the ACK queue

	handlerMu sync.RWMutex
	handlers  map[messageType]responseHandler

	version VersionNum // sent in headers; established during negotiation
}

type messageID uint32
type messageType uint16
type awaitMap = map[messageID]chan<- message
type responseHandler interface {
	handle(r *message)
}

type VersionNum uint8 // only 3 bits legal
const (
	versionInvalid = VersionNum(0)
	Version1_0_1   = VersionNum(1)
	// Version1_1     = VersionNum(2)

	versionMin = Version1_0_1 // min version we support
	versionMax = Version1_0_1 // max version we support
)

// NewReader returns a Reader configured by the given options.
func NewReader(opts ...ReaderOpt) (*Reader, error) {
	// todo: allow connection timeout parameters;
	//   call SetReadDeadline/SetWriteDeadline as needed

	// ackQueueSz is arbitrary, but if it fills,
	// warnings are logged, as it likely indicates a Write problem.
	// At some point, we might consider resetting the connection.
	const ackQueueSz = 5

	r := &Reader{
		version:   versionMax,
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
		sendQueue: make(chan request),
		ackQueue:  make(chan messageID, ackQueueSz),
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

	if r.version < versionMin || r.version > versionMax {
		return nil, errors.Errorf("unsupported version: %v", r.version)
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
func WithVersion(v VersionNum) ReaderOpt {
	if v < versionMin || v > versionMax {
		panic(errors.Errorf("unsupported version %v", v))
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

// stdGoLogger uses a standard Go logger to satisfy the ReaderLogger interface.
var stdGoLogger = log.New(os.Stderr, "LLRP-", log.LstdFlags)

var (
	// ErrReaderClosed is returned if an operation is attempted on a closed Reader.
	// It may be wrapped, so to check for it, use errors.Is.
	ErrReaderClosed = goErrs.New("reader closed")

	// errAlreadyNegotiated is returned if negotiate is called on an open connection.
	errAlreadyNegotiated = goErrs.New("connection is already negotiated")
)

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

	errs := make(chan error, 2)
	go func() { errs <- r.handleOutgoing() }()

	// TODO: there's a race to fix between handleIncoming and negotiation
	m, err := r.nextMessage()
	if err != nil {
		return err
	}
	if err := m.Close(); err != nil {
		return errors.Wrap(err, "negotiate failed")
	}
	close(r.ready)

	go func() { errs <- r.handleIncoming() }()

	select {
	case err = <-errs:
	case <-r.done:
		err = ErrReaderClosed
	}

	return err
}

// Shutdown attempts to gracefully close the connection.
//
// Using Shutdown allows in-progress and queued requests to complete
// and sends the LLRP CloseConnection message.
// You can use this in combination with `Close` to attempt a graceful shutdown,
// followed by a forced shutdown if necessary:
//
//     ctx, cancel = context.WithTimeout(context.Background(), time.Second)
//	   defer cancel()
//	   if err := r.Shutdown(ctx); err != nil {
//	       if !errors.Is(err, context.DeadlineExceeded) {
//             panic(err)
//         }
//         // It's still possible to get ErrReaderClosed
//	       if err := r.Close(); err != nil && !errors.Is(err, ErrReaderClosed) {
//		       panic(err)
//         }
//     }
func (r *Reader) Shutdown(ctx context.Context) error {
	_, err := r.SendMessage(ctx, CloseConnection, nil)
	if err != nil {
		return err
	}

	// todo: process the response, as it can contain status information

	return r.Close()
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
func (r *Reader) SendMessage(ctx context.Context, typ messageType, data []byte) ([]byte, error) {
	select {
	case <-r.ready: // ensure the connection is negotiated
	case <-r.done:
		return nil, ErrReaderClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	default:
	case <-r.done:
		return nil, ErrReaderClosed
	}

	var mOut message
	if len(data) == 0 {
		mOut = newHdrOnlyMsg(typ)
	} else {
		var err error
		mOut, err = newByteMessage(typ, data)
		if err != nil {
			return nil, err
		}
	}

	resp, err := r.send(ctx, mOut)
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	if resp.typ == ErrorMessage {
		/* todo
		if err := resp.parseErr(); err != nil {
			return nil, errors.WithMessage(err,
				"received ErrorMessage, but unable to parse it")
		}
		*/
	}

	if resp.payloadLen > maxBufferedPayloadSz {
		return nil, errors.Errorf("message payload exceeds max: %d > %d",
			resp.payloadLen, maxBufferedPayloadSz)
	}

	buffResponse := make([]byte, resp.payloadLen)
	if _, err := io.ReadFull(resp.payload, buffResponse); err != nil {
		return nil, err
	}

	return buffResponse, nil
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
	buf := make([]byte, headerSz)
	if _, err = io.ReadFull(r.conn, buf); err == nil {
		err = mh.UnmarshalBinary(buf)
	}
	err = errors.Wrap(err, "read header failed")
	return
}

// nextMessage is a convenience method to get a message
// with the payload pointing to the reader's connection.
// It should only be used by internal clients.
func (r *Reader) nextMessage() (message, error) {
	hdr, err := r.readHeader()
	if err != nil {
		return message{}, err
	}
	p := io.LimitReader(r.conn, int64(hdr.payloadLen))
	return message{header: hdr, payload: p}, nil
}

// handleIncoming handles the read side of the connection.
//
// If it encounters an error, it stops and returns it.
// Otherwise, it serves messages until the Reader is closed,
// at which point it and all future calls return ErrReaderClosed.
//
// If this function sees a CloseConnectionResponse,
// it will attempt to continue reading from the connection,
// but if reading a header results in EOF and no data,
// it will return ErrReaderClosed.
//
// Responses to requests are streamed to their sender.
// See send for information about sending messages/receiving responses.
//
// KeepAlive messages are acknowledged automatically as soon as possible.
//
// todo: handle asynchronous reports
// todo: SetReadDeadline
func (r *Reader) handleIncoming() error {
	receivedClosed := false
	for {
		select {
		case <-r.done:
			return ErrReaderClosed
		default:
		}

		m, err := r.nextMessage()

		if err != nil {
			if errors.Is(err, io.EOF) && receivedClosed {
				return ErrReaderClosed
			}
			return err
		}

		if m.typ == CloseConnectionResponse {
			receivedClosed = true
		}

		if m.version != r.version {
			r.logger.Printf("warning: version mismatch (reader %v): %v", r.version, m)
		}

		// Handle the payload.
		handler := r.getResponseHandler(m.header)
		_, err = io.Copy(handler, m.payload)
		if pw, ok := handler.(*io.PipeWriter); ok {
			pw.Close()
		}

		switch err {
		case nil:
		case io.ErrClosedPipe:
			// The message handler stopped listening; that's OK.
		case io.EOF:
			// The connection may have closed.
			return errors.Wrap(io.ErrUnexpectedEOF, "failed to read full payload")
		default:
			return errors.Wrap(err, "failed to process response")
		}

		// Make sure we read the rest of the payload.
		if err := m.Close(); err != nil {
			return err
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
		var msg message

		select {
		case <-r.done:
			return ErrReaderClosed
		case mid = <-r.ackQueue:
			msg = message{header: header{typ: KeepAliveAck}}
		default:
			select {
			case <-r.done:
				return ErrReaderClosed
			case mid = <-r.ackQueue:
				msg = message{header: header{typ: KeepAliveAck}}
			case req := <-r.sendQueue:
				msg = req.msg

				// Generate the message ID.
				mid = nextMsgID
				nextMsgID++

				// Give the read-side a way to correlate the response
				// with something the sender can listen to.
				replyChan := make(chan message, 1)
				r.awaitMu.Lock()
				r.awaiting[mid] = replyChan
				r.awaitMu.Unlock()

				// Give the sender a way to clean up
				// in case a response never comes.
				token := sendToken{
					replyChan: replyChan,
					cancel: func() {
						r.awaitMu.Lock()
						defer r.awaitMu.Unlock()
						if c, ok := r.awaiting[mid]; ok {
							close(c)
							delete(r.awaiting, mid)
						}
					},
				}

				// Give those to the sender.
				req.tokenChan <- token
				close(req.tokenChan)
			}
		}

		if err := r.writeHeader(mid, msg.payloadLen, msg.typ); err != nil {
			return err
		}

		if msg.payloadLen == 0 {
			continue
		}

		if msg.payload == nil {
			return errors.Errorf("message data is nil, but has length >0 (%v)", msg)
		}

		if n, err := io.Copy(r.conn, msg.payload); err != nil {
			return errors.Wrapf(err, "write failed after %d bytes for %v", n, msg)
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
func (r *Reader) getResponseHandler(hdr header) io.Writer {
	// todo: handle LLRP error message type
	if hdr.typ == KeepAlive {
		r.sendAck(hdr.id)
		return ioutil.Discard
	}

	r.awaitMu.Lock()
	replyChan, ok := r.awaiting[hdr.id]
	delete(r.awaiting, hdr.id)
	r.awaitMu.Unlock()

	if !ok {
		return ioutil.Discard
	}

	pr, pw := io.Pipe()
	resp := message{header: hdr, payload: pr}
	replyChan <- resp
	close(replyChan)
	return pw
}

// request is sent to the write coordinator to start a new request.
// A sender puts a request in the sendQueue with a valid tokenChan.
// When the write coordinator processes the request,
// it'll use the channel to send back response/cancellation data.
type request struct {
	tokenChan chan<- sendToken // closed by the write coordinator
	msg       message          // the message to send
}

// sendToken is sent back to the sender in response to a send request.
type sendToken struct {
	replyChan <-chan message // closed by the read coordinator
	cancel    func()         // The sender should call this if it stops waiting for the reply.
}

// send a message as soon as possible and wait for its response.
// The reader must be connected and serving the incoming/outgoing queues.
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
// (see SendMessage for the primary external API).
// This method streams the outgoing and incoming data
// to avoid duplicating the memory of outgoing messages
// or processing unwanted responses, in full or in part.
// The sender gets exclusive access to part of the connection,
// so blocking, panics, or writing invalid data can break the connection
// or stop other senders from sending/receiving messages.
//
// If err is non-nil, the response payload is non-nil;
// however, if payloadLen is zero, it will return EOF immediately.
func (r *Reader) send(ctx context.Context, m message) (message, error) {
	// The write coordinator sends us a token to read or cancel the reply.
	// We shouldn't close this channel once the request is accepted.
	tokenChan := make(chan sendToken, 1)
	req := request{msg: m, tokenChan: tokenChan}

	// Wait until the message is sent, unless the Reader is closed.
	select {
	case <-r.done:
		close(tokenChan)
		return message{}, errors.Wrap(ErrReaderClosed, "message not sent")
	case <-ctx.Done():
		close(tokenChan)
		return message{}, ctx.Err()
	case r.sendQueue <- req:
		// The message was accepted; we can no longer cancel sending.
	}

	token := <-tokenChan // This should complete basically immediately.

	// Now we wait for the reply.
	select {
	case <-r.done:
		token.cancel()
		return message{}, errors.Wrap(ErrReaderClosed, "message sent, but not awaited")
	case <-ctx.Done():
		token.cancel()
		return message{}, ctx.Err()
	case resp := <-token.replyChan:
		return resp, nil
	}
}

// negotiate performs version negotiation with a Reader.
// Upon success, it closes the "ready" channel
// to signal the Reader is ready to accept external requests.
//
// If called on a Reader that previously opened and negotiated a connection,
// this returns errAlreadyNegotiated.
func (r *Reader) negotiate(m *message) (err error) {
	select {
	default:
	case <-r.ready:
		return errAlreadyNegotiated
	}

	select {
	default:
	case <-r.done:
		return ErrReaderClosed
	}

	// todo: unmarshal message; check for ConnectionSuccessful

	if err := m.Close(); err != nil {
		return errors.Wrap(err, "negotiate failed")
	}
	defer close(r.ready)

	// In version 1, there is no version negotiation.
	if r.version == Version1_0_1 {
		return nil
	}

	resp, err := r.send(context.TODO(), newHdrOnlyMsg(GetSupportedVersion))
	if err != nil {
		return errors.WithMessage(err, "failed to Get Supported Versions")
	}

	// todo: unmarshal message; check version
	if err := resp.isResponseTo(GetSupportedVersion); err != nil {
		return errors.WithMessage(err, "failed to Get Supported Versions")
	}

	if err = resp.Close(); err != nil {
		return err
	}

	resp, err = r.send(context.TODO(), newHdrOnlyMsg(SetProtocolVersion))
	if err != nil {
		return errors.WithMessage(err, "failed to Set Protocol Version")
	}

	// todo: unmarshal message; confirm version match or downgrade
	if err := resp.isResponseTo(SetProtocolVersion); err != nil {
		return errors.WithMessage(err, "failed to Set Protocol Version")
	}

	return resp.Close()
}
