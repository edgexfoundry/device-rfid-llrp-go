//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

// Package llrp implements the Low Level Reader Protocol (LLRP)
// to communicate with compliant RFID Readers.
//
// This package focuses on Client-side communication,
// and handles the protocol minutia like Keep-Alive acknowledgements
// while providing types to facilitate handling messages you care about.
//
// A typical use of this package focuses on the Reader connection:
// - Create a new Reader with connection details and message handlers.
// - Establish and maintain an LLRP connection with an RFID device.
// - Send and receive LLRP messages.
// - At some point, gracefully close the connection.
//
// Some users may find value in the MsgReader and MsgBuilder types,
// which provide efficient LLRP message translation among binary, Go types, and JSON.
//
// Note that names in LLRP are often verbose and sometimes overloaded.
// These names have been judiciously translated when appropriate
// to better match Go idioms and standards,
// and follow the following conventions for ease of use:
// - Messages' numeric type values are typed as MessageType
//   and match their LLRP name, converted from UPPER_SNAKE to PascalCase.
// - Parameters' numeric type values are typed as ParamType and prefixed with "Param";
//   they typically match the LLRP name with the "Parameter" suffix omitted.
// - LLRP Status Codes are typed as StatusCode and prefixed with "Status".
// - ConnectionAttemptEventParameter's status field is typed as ConnectionStatus
//   and prefixed with "Conn".
package llrp

import (
	"context"
	"encoding/binary"
	"encoding/json"
	goErrs "errors"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
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
	timeout   time.Duration  // if non-zero, sets conn's deadline for reads/writes

	handlerMu sync.RWMutex
	handlers  map[MessageType]responseHandler

	version VersionNum // sent in headers; established during negotiation
}

const (
	versionMin = Version1_0_1 // min version we support
	versionMax = Version1_1   // max version we support
)

// NewReader returns a Reader configured by the given options.
func NewReader(opts ...ReaderOpt) (*Reader, error) {
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
		handlers:  make(map[MessageType]responseHandler),
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
		r.logger = log.New(os.Stderr, "LLRP-"+r.conn.RemoteAddr().String()+"-", log.LstdFlags)
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

// WithTimeout sets a timeout for reads/writes.
// If non-zero, the reader will call SetReadDeadline and SetWriteDeadline
// before reading or writing respectively.
func WithTimeout(d time.Duration) ReaderOpt {
	return readerOpt(func(r *Reader) error {
		if d < 0 {
			return errors.Errorf("timeout should be at least 0, but is %v", d)
		}
		r.timeout = d
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

	if err := r.checkInitialMessage(); err != nil {
		return err
	}

	errs := make(chan error, 2)
	go func() { errs <- r.handleOutgoing() }()
	go func() { errs <- r.handleIncoming() }()

	if r.version > Version1_0_1 {
		if err := r.negotiate(); err != nil {
			return err
		}
	}

	close(r.ready)

	var err error
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
	r.logger.Println("putting CloseConnection in send queue")
	rTyp, resp, err := r.SendMessage(ctx, CloseConnection, nil)
	if err != nil {
		return err
	}

	r.logger.Println("handling response to CloseConnection")

	ls := llrpStatus{}
	switch rTyp {
	case CloseConnectionResponse:
		ccr := closeConnectionResponse{}
		if err := ccr.UnmarshalBinary(resp); err != nil {
			return errors.WithMessage(err, "unable to read CloseConnectionResponse")
		}
		ls = ccr.LLRPStatus
	case ErrorMessage:
		if err := ls.UnmarshalBinary(resp); err != nil {
			return errors.WithMessage(err, "unable to read ErrorMessage response")
		}
	default:
		return errors.Errorf("unexpected response to CloseConnection: %v", rTyp)
	}

	if err := ls.Err(); err != nil {
		return errors.WithMessage(err, "reader rejected CloseConnection")
	}

	return r.Close()
}

// Close closes the Reader immediately.
//
// See Shutdown for an attempt to close the connection gracefully.
// After closing, the Reader can no longer serve connections.
func (r *Reader) Close() error {
	if atomic.CompareAndSwapUint32(&r.isClosed, 0, 1) {
		close(r.done)
		return nil
	} else {
		return errors.Wrap(ErrReaderClosed, "close")
	}
}

const maxBufferedPayloadSz = uint32((1 << 10) * 640)

// SendMessage sends the given data, assuming it matches the type.
// It returns the response data or an error.
func (r *Reader) SendMessage(ctx context.Context, typ MessageType, data []byte) (MessageType, []byte, error) {
	select {
	case <-r.ready: // ensure the connection is negotiated
	case <-r.done:
		return 0, nil, errors.Wrap(ErrReaderClosed, "message not sent")
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}

	var mOut Message
	if len(data) == 0 {
		mOut = NewHdrOnlyMsg(typ)
	} else {
		var err error
		mOut, err = NewByteMessage(typ, data)
		if err != nil {
			return 0, nil, err
		}
	}

	resp, err := r.send(ctx, mOut)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Close()

	if resp.payloadLen > maxBufferedPayloadSz {
		return 0, nil, errors.Errorf("message payload exceeds max: %d > %d",
			resp.payloadLen, maxBufferedPayloadSz)
	}

	buffResponse := make([]byte, resp.payloadLen)
	if _, err := io.ReadFull(resp.payload, buffResponse); err != nil {
		return 0, nil, err
	}

	return resp.typ, buffResponse, nil
}

// writeHeader writes a message header to the connection.
//
// It does not validate the parameters,
// as it assumes its already been done.
//
// Once a message header is written,
// length bytes must also be written to the stream,
// or the connection will be in an invalid state and should be closed.
func (r *Reader) writeHeader(h header) error {
	header := make([]byte, headerSz)
	binary.BigEndian.PutUint32(header[6:10], uint32(h.id))
	binary.BigEndian.PutUint32(header[2:6], h.payloadLen+headerSz)
	binary.BigEndian.PutUint16(header[0:2], uint16(h.version)<<10|uint16(h.typ))
	_, err := r.conn.Write(header)
	return errors.Wrapf(err, "failed to write header")
}

// readHeader returns the next message header from the connection.
//
// It assumes that the next bytes on the incoming stream are a header,
// and that nothing else is attempting to read from the connection.
//
// If the Reader has a timeout, this will set the read deadline before reading.
// This method blocks until it reads enough bytes to fill the header buffer
// unless the underlying connection is closed or times out.
func (r *Reader) readHeader() (mh header, err error) {
	if r.timeout > 0 {
		if err = r.conn.SetReadDeadline(time.Now().Add(r.timeout)); err != nil {
			err = errors.Wrap(err, "failed to set read deadline")
			return
		}
	}

	buf := make([]byte, headerSz)
	if _, err = io.ReadFull(r.conn, buf); err != nil {
		err = errors.Wrap(err, "failed to read header")
		return
	}

	err = errors.WithMessage(mh.UnmarshalBinary(buf), "failed to unmarshal header")
	return
}

// nextMessage is a convenience method to get a message
// with the payload pointing to the reader's connection.
// It should only be used by internal clients,
// as the current message must be read before continuing.
func (r *Reader) nextMessage() (Message, error) {
	hdr, err := r.readHeader()
	if err != nil {
		return Message{}, err
	}
	p := io.LimitReader(r.conn, int64(hdr.payloadLen))
	return Message{header: hdr, payload: p}, nil
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
func (r *Reader) handleIncoming() error {
	receivedClosed := false
	for {
		select {
		case <-r.done:
			return errors.Wrap(ErrReaderClosed, "stopping incoming")
		default:
		}

		m, err := r.nextMessage()
		if err != nil {
			if !receivedClosed {
				return errors.Wrap(err, "failed to get next message")
			}

			if !(errors.Is(err, io.EOF) || os.IsTimeout(err)) {
				return errors.Wrap(err, "timeout")
			}

			r.logger.Println("detected EOF/io.Timeout after CloseConnection; waiting for reader to close")
			<-r.done
			return errors.Wrap(ErrReaderClosed, "client connection closed")
		}

		r.logger.Printf(">>> %v", m)

		if m.typ == CloseConnectionResponse {
			receivedClosed = true
		}

		if m.version != r.version {
			r.logger.Printf("warning: incoming message version mismatch (expected %v): %v", r.version, m)
		}

		// Handle the payload.
		handler := r.getResponseHandler(m.header)
		_, err = io.Copy(handler, m.payload)
		if pw, ok := handler.(*io.PipeWriter); ok {
			pw.Close()
		}

		r.logger.Printf("handled %v", m)

		switch err {
		case nil, io.ErrClosedPipe:
			// If the message handler stopped listening; that's OK.
			// Make sure we read the rest of the payload.
			if err := m.Close(); err != nil {
				return err
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
// If this see CloseConnection, it stops processing messages.
//
// While the connection is open,
// KeepAliveAck messages are prioritized.
func (r *Reader) handleOutgoing() error {
	var nextMsgID messageID

	for {
		// Get the next message to send, giving priority to ACKs.
		var msg Message

		select {
		case <-r.done:
			return errors.Wrap(ErrReaderClosed, "stopping outgoing")
		case mid := <-r.ackQueue:
			msg = Message{header: header{id: mid, typ: KeepAliveAck}}
		default:
			select {
			case <-r.done:
				return errors.Wrap(ErrReaderClosed, "stopping outgoing")
			case mid := <-r.ackQueue:
				msg = Message{header: header{id: mid, typ: KeepAliveAck}}
			case req := <-r.sendQueue:
				msg = req.msg

				// Generate the message ID.
				msg.id = nextMsgID
				nextMsgID++

				// Give the read-side a way to correlate the response
				// with something the sender can listen to.
				replyChan := make(chan Message, 1)
				r.awaitMu.Lock()
				r.awaiting[msg.id] = replyChan
				r.awaitMu.Unlock()

				// Give the sender a way to clean up
				// in case a response never comes.
				mid := msg.id
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

		if msg.typ == GetSupportedVersion || msg.typ == SetProtocolVersion {
			// these messages are required to use version 1.1
			msg.version = Version1_1
		} else {
			msg.version = r.version
		}

		if r.timeout > 0 {
			if err := r.conn.SetWriteDeadline(time.Now().Add(r.timeout)); err != nil {
				return errors.Wrap(err, "failed to set write deadline")
			}
		}

		r.logger.Printf("<<< %v", msg)
		if err := r.writeHeader(msg.header); err != nil {
			return err
		}

		// stop processing messages
		if msg.typ == CloseConnection {
			r.logger.Println("CloseConnection sent; waiting for reader to close.")
			select {
			case <-r.done:
			}
			return errors.Wrap(ErrReaderClosed, "client closed connection")
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
		r.logger.Println("KeepAlive received.")
	default:
		r.logger.Println("Discarding KeepAliveAck as queue is full. " +
			"This may indicate the Reader's write side is broken " +
			"yet not timing out.")
	}
}

// getResponseHandler returns the handler for a given message's response.
func (r *Reader) getResponseHandler(hdr header) io.Writer {
	r.logger.Printf("finding handler for mID %d: %v", hdr.id, hdr.typ)

	if hdr.typ == KeepAlive {
		r.sendAck(hdr.id)
		return ioutil.Discard
	}

	r.awaitMu.Lock()
	replyChan, ok := r.awaiting[hdr.id]
	delete(r.awaiting, hdr.id)
	r.awaitMu.Unlock()

	if !ok {
		r.logger.Printf("no handler found for mID %d: %v", hdr.id, hdr.typ)
		return ioutil.Discard
	}

	r.logger.Printf("piping mID %d: %v", hdr.id, hdr.typ)
	// the pipe lets us know when the other side finishes
	pr, pw := io.Pipe()
	resp := Message{header: hdr, payload: pr}
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
	msg       Message          // the message to send
}

// sendToken is sent back to the sender in response to a send request.
type sendToken struct {
	replyChan <-chan Message // closed by the read coordinator
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
func (r *Reader) send(ctx context.Context, m Message) (Message, error) {
	// The write coordinator sends us a token to read or cancel the reply.
	// We shouldn't close this channel once the request is accepted.
	tokenChan := make(chan sendToken, 1)
	req := request{msg: m, tokenChan: tokenChan}

	// Wait until the message is sent, unless the Reader is closed.
	select {
	case <-r.done:
		close(tokenChan)
		return Message{}, errors.Wrap(ErrReaderClosed, "message not sent")
	case <-ctx.Done():
		close(tokenChan)
		return Message{}, ctx.Err()
	case r.sendQueue <- req:
		// The message was accepted; we can no longer cancel sending.
	}

	token := <-tokenChan // This should complete basically immediately.

	// Now we wait for the reply.
	select {
	case <-r.done:
		token.cancel()
		return Message{}, errors.Wrap(ErrReaderClosed, "message sent, but not awaited")
	case <-ctx.Done():
		token.cancel()
		return Message{}, ctx.Err()
	case resp := <-token.replyChan:
		return resp, nil
	}
}

func (r *Reader) checkInitialMessage() error {
	m, err := r.nextMessage()
	if err != nil {
		return err
	}
	defer m.Close()

	r.logger.Printf(">>> (initial) %v", m)
	if m.typ != ReaderEventNotification {
		return errors.Errorf("expected %v, but got %v", ReaderEventNotification, m.typ)
	}

	buf := make([]byte, m.payloadLen)
	if _, err := io.ReadFull(m.payload, buf); err != nil {
		return errors.Wrap(err, "failed to read message payload")
	}

	ren := readerEventNotification{}
	if err := ren.UnmarshalBinary(buf); err != nil {
		return errors.Wrap(err, "failed to unmarshal ReaderEventNotification")
	}

	if j, err := json.MarshalIndent(ren, "", "\t"); err == nil {
		r.logger.Printf(">>> (initial) %s", j)
	}

	if ren.isConnectSuccess() {
		return errors.Wrapf(err, "connection not successful: %+v", ren)
	}

	return m.Close()
}

// getSupportedVersion returns device's current and supported LLRP versions.
//
// This message was added in LLRP v1.1,
// so v1.0.1 devices should return ErrorMessage with VersionUnsupported status,
// but this method handles that case and returns a valid SupportedVersion struct.
// As a result, error is only not nil when network communication or message processing fails;
// if the error is not nil, the returned struct will indicate the correct version information.
func (r *Reader) getSupportedVersion(ctx context.Context) (*getSupportedVersionResponse, error) {
	resp, err := r.send(ctx, NewHdrOnlyMsg(GetSupportedVersion))
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	data := make([]byte, resp.payloadLen)
	if _, err := io.ReadFull(resp.payload, data); err != nil {
		return nil, err
	}
	if err := resp.Close(); err != nil {
		return nil, err
	}

	// By default, we'll return version 1.0.1.
	sv := getSupportedVersionResponse{
		CurrentVersion:      Version1_0_1,
		MaxSupportedVersion: Version1_0_1,
	}

	// Every response message that includes an LLRPStatus parameter
	// puts the status first _except_ GetSupportedVersionResponse,
	// for some silly reason.
	// As a result, we have to check the type to know what parts to decode.
	switch resp.typ {
	default:
		return nil, errors.Errorf("unexpected response to %v: %v", GetSupportedVersion, resp)
	case ErrorMessage:
		errMsg := errorMessage{}
		// If the reader only supports v1.0.1, it returns VersionUnsupported.
		// In that case, we'll drop the error so we can treat all results identically.
		if err := errMsg.UnmarshalBinary(data); err != nil {
			return nil, err
		}

		sv.LLRPStatus = errMsg.LLRPStatus

		if sv.LLRPStatus.Status == StatusMsgVerUnsupported {
			sv.LLRPStatus = llrpStatus{Status: StatusSuccess}
		}

	case GetSupportedVersionResponse:
		if err := sv.UnmarshalBinary(data); err != nil {
			return nil, err
		}
	}

	return &sv, errors.WithMessagef(sv.LLRPStatus.Err(), "%v returned an error", resp)
}

// negotiate LLRP versions with the RFID device.
//
// Upon success, this sets the Reader's version to match the negotiated value.
// The Reader will use the lesser of its configured version and the device's version,
// or LLRP v1.0.1 if the device does not support version negotiation.
// By default, newly created Readers use the max version supported by this package.
func (r *Reader) negotiate() error {
	sv, err := r.getSupportedVersion(context.Background())
	if err != nil {
		return err
	}

	if sv.CurrentVersion == r.version {
		r.logger.Printf("device version matches Reader: %v", r.version)
		return nil
	}

	ver := r.version
	if r.version > sv.MaxSupportedVersion {
		ver = sv.MaxSupportedVersion
		r.logger.Printf("downgrading Reader version to match device max: %v", ver)
		r.version = ver

		// if the device is already using this, no need to set it
		if sv.CurrentVersion == r.version {
			return nil
		}
	}

	r.logger.Printf("requesting device use version %v", ver)

	m, err := NewByteMessage(SetProtocolVersion, []byte{uint8(r.version)})
	if err != nil {
		return err
	}

	resp, err := r.send(context.Background(), m)
	if err != nil {
		return err
	}
	defer resp.Close()

	if err := resp.isResponseTo(SetProtocolVersion); err != nil {
		return err
	}

	return resp.Close()
}
