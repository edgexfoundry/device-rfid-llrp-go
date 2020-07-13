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
// A typical use of this package focuses on the Client connection:
// - Create a new Client with connection details and message handlers.
// - Establish and maintain an LLRP connection with an RFID device.
// - Send and receive LLRP messages.
// - At some point, gracefully close the connection.
//
// The package provides methods that parse and validate
// LLRP message to translate them among binary, Go types, and JSON.
//
// Note that names in LLRP are often verbose and sometimes overloaded.
// These names have been judiciously translated when appropriate
// to better match Go idioms and standards.
package llrp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	goErrs "errors"
	"fmt"
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

// Client represents a client connection to an LLRP-compatible RFID reader.
type Client struct {
	Name           string         // arbitrary
	conn           net.Conn       // underlying network connection
	sendQueue      chan request   // controls write-side of connection
	ackQueue       chan messageID // gesundheit -- allows ACK'ing fast, unless sendQueue is unhealthy
	awaitMu        sync.Mutex     // synchronize awaiting map access
	awaiting       awaitMap       // message IDs -> awaiting reply
	logger         ClientLogger   // reports pressure on the ACK queue
	handlers       map[MessageType]MessageHandler
	defaultHandler MessageHandler // used if no MessageHandlers for type and nothing awaiting reply
	timeout        time.Duration  // if non-zero, sets conn's deadline for reads/writes
	done           chan struct{}  // closed when the Client is closed
	ready          chan struct{}  // closed when the connection is negotiated
	isClosed       uint32         // used atomically to prevent duplicate closure of done
	version        VersionNum     // sent in headers; established during negotiation
}

const (
	versionMin = Version1_0_1 // min version we support
	versionMax = Version1_1   // max version we support
)

// NewClient returns a Client configured by the given options.
func NewClient(opts ...ClientOpt) (*Client, error) {
	// ackQueueSz is arbitrary, but if it fills,
	// warnings are logged, as it likely indicates a Write problem.
	// At some point, we might consider resetting the connection.
	const ackQueueSz = 5

	r := &Client{
		version:   versionMax,
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
		sendQueue: make(chan request),
		ackQueue:  make(chan messageID, ackQueueSz),
		awaiting:  make(awaitMap),
		handlers: map[MessageType]MessageHandler{
			MsgKeepAlive:      ackHandler{},
			MsgROAccessReport: roHandler{},
		},
	}

	for _, opt := range opts {
		if err := opt.do(r); err != nil {
			return nil, err
		}
	}

	if r.conn == nil {
		return nil, errors.New("Client has no connection")
	}

	if r.version < versionMin || r.version > versionMax {
		return nil, errors.Errorf("unsupported version: %v", r.version)
	}

	if r.logger == nil {
		r.logger = log.New(os.Stderr, "LLRP-"+r.conn.RemoteAddr().String()+"-", log.LstdFlags)
	}

	return r, nil
}

// ClientOpt modifies a Client during construction.
type ClientOpt interface {
	do(*Client) error // don't allow arbitrary implementations for now
}

type clientOpt func(c *Client) error

func (ro clientOpt) do(c *Client) error {
	return ro(c)
}

// WithConn sets the Client's connection.
func WithConn(conn net.Conn) ClientOpt {
	return clientOpt(func(c *Client) error {
		c.conn = conn
		return nil
	})
}

// WithName sets the Client's name.
func WithName(name string) ClientOpt {
	return clientOpt(func(c *Client) error {
		c.Name = name
		return nil
	})
}

// WithVersion sets the expected LLRP version number.
// The actual version number used during communication is selected when connecting.
// Currently, only version 1 is supported; others will panic.
func WithVersion(v VersionNum) ClientOpt {
	if v < versionMin || v > versionMax {
		panic(errors.Errorf("unsupported version %v", v))
	}
	return clientOpt(func(c *Client) error {
		c.version = v
		return nil
	})
}

// WithTimeout sets the connection timeout for all reads and writes.
//
// If non-zero, the client will call SetReadDeadline and SetWriteDeadline
// before reading or writing respectively.
//
// Note that if set, the connection will automatically close
// if the RFID device fails to send a message within the timeout.
// As a result, this is most useful when combined with LLRP KeepAlive,
// and set to an integer multiple of the KeepAlive interval plus a small grace.
// In that case, the client will automatically close the connection
// if it fails to receive KeepAlive messages from the RFID device.
func WithTimeout(d time.Duration) ClientOpt {
	return clientOpt(func(c *Client) error {
		if d < 0 {
			return errors.Errorf("timeout should be at least 0, but is %v", d)
		}
		c.timeout = d
		return nil
	})
}

// WithLogger sets a logger for the Client.
func WithLogger(l ClientLogger) ClientOpt {
	return clientOpt(func(c *Client) error {
		c.logger = l
		return nil
	})
}

type MessageHandler interface {
	handleMessage(c *Client, msg Message)
}

type MessageHandlerFunc func(c *Client, msg Message)

func (mhf MessageHandlerFunc) handleMessage(c *Client, msg Message) {
	mhf(c, msg)
}

// WithMessageHandler sets a handler which will be called
// whenever a message of the given type arrives.
//
// Each message type has only a single handler,
// so if you wish to support multiple behaviors for a single type,
// you'll need to copy and propagate the message to the other handlers.
// Setting a handler for the same message more than once
// will overwrite previously recorded handlers.
//
// Clients are created with a handler for KeepAlive (it sends KeepAliveAck).
// If you override this, you'll need to acknowledge the KeepAlives yourself.
//
// If a the incoming message is a response with a listener awaiting the rely,
// the message is copied to a buffer, sent to the response listener,
// then dispatched to the handler.
//
// Currently, there isn't a way to remove a handler,
// nor to add them after a Client is created,
// though that may change in the future.
//
// Adding a nil handler clears it.
func WithMessageHandler(mt MessageType, handler MessageHandler) ClientOpt {
	return clientOpt(func(c *Client) error {
		if handler == nil {
			delete(c.handlers, mt)
		} else {
			c.handlers[mt] = handler
		}
		return nil
	})
}

// WithDefaultHandler sets a default handler for messages
// that aren't handled by either an awaiting listener or another MessageHandler.
//
// Only a single default handler is supported,
// though it may propagate the message to other handlers if desired.
//
// It may be set to nil, in which case unhandled messages are simply dropped.
func WithDefaultHandler(handler MessageHandler) ClientOpt {
	return clientOpt(func(c *Client) error {
		c.defaultHandler = handler
		return nil
	})
}

// ClientLogger is used by the Client to log certain status messages.
type ClientLogger interface {
	Println(v ...interface{})
	Printf(fmt string, v ...interface{})
}

var (
	// ErrClientClosed is returned if an operation is attempted on a closed Client.
	// It may be wrapped, so to check for it, use errors.Is.
	ErrClientClosed = goErrs.New("client closed")
)

// Connect to an LLRP-capable device and start processing messages.
//
// This takes ownership of the connection,
// which it assumes is already dialed.
// It will serve the connection's incoming and outgoing messages
// until it encounters an error or the Client is closed.
// Before returning, it closes the connection.
//
// If the Client is closed, this returns ErrClientClosed;
// otherwise, it returns the first error it encounters.
func (c *Client) Connect() error {
	defer c.conn.Close()
	defer c.Close()

	if err := c.checkInitialMessage(); err != nil {
		return err
	}

	errs := make(chan error, 2)
	go func() { errs <- c.handleOutgoing() }()
	go func() { errs <- c.handleIncoming() }()

	if c.version > Version1_0_1 {
		if err := c.negotiate(); err != nil {
			return err
		}
	}

	close(c.ready)

	var err error
	select {
	case err = <-errs:
	case <-c.done:
		err = ErrClientClosed
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
//	       if err := r.Close(); err != nil && !errors.Is(err, ErrClientClosed) {
//		       panic(err)
//         }
//     }
func (c *Client) Shutdown(ctx context.Context) error {
	c.logger.Println("putting CloseConnection in send queue")
	rTyp, resp, err := c.SendMessage(ctx, MsgCloseConnection, nil)
	if err != nil {
		return err
	}

	c.logger.Println("handling response to CloseConnection")

	ls := LLRPStatus{}
	switch rTyp {
	case MsgCloseConnectionResponse:
		ccr := CloseConnectionResponse{}
		if err := ccr.UnmarshalBinary(resp); err != nil {
			return errors.WithMessage(err, "unable to read CloseConnectionResponse")
		}
		ls = ccr.LLRPStatus
	case MsgErrorMessage:
		if err := ls.UnmarshalBinary(resp); err != nil {
			return errors.WithMessage(err, "unable to read ErrorMessage response")
		}
	default:
		return errors.Errorf("unexpected response to CloseConnection: %v", rTyp)
	}

	if err := ls.Err(); err != nil {
		return errors.WithMessage(err, "reader rejected CloseConnection")
	}

	return c.Close()
}

// Close closes the Client's connection to the reader immediately.
//
// See Shutdown for an attempt to close the connection gracefully.
// After closing, the Client can no longer serve connections.
func (c *Client) Close() error {
	if atomic.CompareAndSwapUint32(&c.isClosed, 0, 1) {
		close(c.done)
		return nil
	} else {
		return errors.Wrap(ErrClientClosed, "close")
	}
}

const MaxBufferedPayloadSz = uint32((1 << 10) * 640)

// SendMessage sends the given data, assuming it matches the type.
// It returns the response data or an error.
func (c *Client) SendMessage(ctx context.Context, typ MessageType, data []byte) (MessageType, []byte, error) {
	select {
	case <-c.ready: // ensure the connection is negotiated
	case <-c.done:
		return 0, nil, errors.Wrap(ErrClientClosed, "message not sent")
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

	resp, err := c.send(ctx, mOut)
	if err != nil {
		return 0, nil, err
	}

	respData, err := resp.data()
	if err != nil {
		return 0, nil, err
	}

	return resp.typ, respData, nil
}

// writeHeader writes a message header to the connection.
//
// It does not validate the parameters,
// as it assumes its already been done.
//
// Once a message header is written,
// length bytes must also be written to the stream,
// or the connection will be in an invalid state and should be closed.
func (c *Client) writeHeader(h Header) error {
	header := make([]byte, HeaderSz)
	binary.BigEndian.PutUint32(header[6:10], uint32(h.id))
	binary.BigEndian.PutUint32(header[2:6], h.payloadLen+HeaderSz)
	binary.BigEndian.PutUint16(header[0:2], uint16(h.version)<<10|uint16(h.typ))
	_, err := c.conn.Write(header)
	return errors.Wrapf(err, "failed to write header")
}

// readHeader returns the next message header from the connection.
//
// It assumes that the next bytes on the incoming stream are a header,
// and that nothing else is attempting to read from the connection.
//
// If the Client has a timeout, this will set the read deadline before reading.
// This method blocks until it reads enough bytes to fill the header buffer
// unless the underlying connection is closed or times out.
func (c *Client) readHeader() (mh Header, err error) {
	if c.timeout > 0 {
		if err = c.conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
			err = errors.Wrap(err, "failed to set read deadline")
			return
		}
	}

	buf := make([]byte, HeaderSz)
	if _, err = io.ReadFull(c.conn, buf); err != nil {
		err = errors.Wrap(err, "failed to read header")
		return
	}

	err = errors.WithMessage(mh.UnmarshalBinary(buf), "failed to unmarshal header")
	return
}

// nextMessage is a convenience method to get a message
// with the payload pointing to the client's connection.
// It should only be used by internal callers,
// as the current message must be read before continuing.
func (c *Client) nextMessage() (Message, error) {
	hdr, err := c.readHeader()
	if err != nil {
		return Message{}, err
	}
	p := io.LimitReader(c.conn, int64(hdr.payloadLen))
	return Message{Header: hdr, payload: p}, nil
}

// handleIncoming handles the read side of the connection.
//
// If it encounters an error, it stops and returns it.
// Otherwise, it serves messages until the Client is closed,
// at which point it and all future calls return ErrClientClosed.
//
// If this function sees a CloseConnectionResponse,
// it will attempt to continue reading from the connection,
// but if reading a header results in EOF and no data,
// it will return ErrClientClosed.
//
// Responses to requests are streamed to their sender.
// See send for information about sending messages/receiving responses.
//
// KeepAlive messages are acknowledged automatically as soon as possible.
func (c *Client) handleIncoming() error {
	receivedClosed := false
	for {
		select {
		case <-c.done:
			return errors.Wrap(ErrClientClosed, "stopping incoming")
		default:
		}

		hdr, err := c.readHeader()
		if err != nil {
			if !receivedClosed {
				return errors.Wrap(err, "failed to get next message")
			}

			if !(errors.Is(err, io.EOF) || os.IsTimeout(err)) {
				return errors.Wrap(err, "timeout")
			}

			c.logger.Println("detected EOF/io.Timeout after CloseConnection; waiting for reader to close")
			<-c.done
			return errors.Wrap(ErrClientClosed, "client connection closed")
		}

		c.logger.Printf(">>> message{%v}", hdr)

		if hdr.typ == MsgCloseConnectionResponse {
			receivedClosed = true
		}

		if hdr.version != c.version {
			c.logger.Printf("warning: incoming message version mismatch (expected %v): %v", c.version, hdr)
		}

		// Handle the payload.
		err = c.passToHandler(hdr)
		c.logger.Printf("handled %v", hdr)

		switch err {
		case nil, io.ErrClosedPipe:
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
// Otherwise, it serves messages until the Client is closed,
// at which point it and all future calls return ErrClientClosed.
// If this see CloseConnection, it stops processing messages.
//
// While the connection is open,
// KeepAliveAck messages are prioritized.
func (c *Client) handleOutgoing() error {
	var nextMsgID messageID

	for {
		// Get the next message to send, giving priority to ACKs.
		var msg Message

		select {
		case <-c.done:
			return errors.Wrap(ErrClientClosed, "stopping outgoing")
		case mid := <-c.ackQueue:
			msg = Message{Header: Header{id: mid, typ: MsgKeepAliveAck}}
		default:
			select {
			case <-c.done:
				return errors.Wrap(ErrClientClosed, "stopping outgoing")
			case mid := <-c.ackQueue:
				msg = Message{Header: Header{id: mid, typ: MsgKeepAliveAck}}
			case req := <-c.sendQueue:
				msg = req.msg

				// Generate the message ID if the message doesn't have one.
				// This assumes you'll never reply to a message with ID 0.
				if msg.id == 0 {
					msg.id = nextMsgID
					nextMsgID++
				}

				// If the reply is unwanted (or unexpected), skip setting it up.
				if req.tokenChan == nil {
					break
				}

				// Give the read-side a way to correlate the response
				// with something the sender can listen to.
				replyChan := make(chan Message, 1)
				c.awaitMu.Lock()
				c.awaiting[msg.id] = replyChan
				c.awaitMu.Unlock()

				// Give the sender a way to clean up
				// in case a response never comes.
				mid := msg.id
				token := sendToken{
					replyChan: replyChan,
					cancel: func() {
						c.awaitMu.Lock()
						defer c.awaitMu.Unlock()
						if replyCh, ok := c.awaiting[mid]; ok {
							close(replyCh)
							delete(c.awaiting, mid)
						}
					},
				}

				// Give those to the sender.
				req.tokenChan <- token
				close(req.tokenChan)
			}
		}

		if msg.typ == MsgGetSupportedVersion || msg.typ == MsgSetProtocolVersion {
			// these messages are required to use version 1.1
			msg.version = Version1_1
		} else if msg.version == 0 {
			msg.version = c.version
		}

		if c.timeout > 0 {
			if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
				return errors.Wrap(err, "failed to set write deadline")
			}
		}

		c.logger.Printf("<<< %v", msg)
		if err := c.writeHeader(msg.Header); err != nil {
			return err
		}

		// stop processing messages
		if msg.typ == MsgCloseConnection {
			c.logger.Println("CloseConnection sent; waiting for reader to close.")
			select {
			case <-c.done:
			}
			return errors.Wrap(ErrClientClosed, "client closed connection")
		}

		if msg.payloadLen == 0 {
			continue
		}

		if msg.payload == nil {
			return errors.Errorf("message data is nil, but has length >0 (%v)", msg)
		}

		if n, err := io.Copy(c.conn, msg.payload); err != nil {
			return errors.Wrapf(err, "write failed after %d bytes for %v", n, msg)
		}
	}
}

// sendAck acknowledges a KeepAlive message with the given message ID.
//
// If the Client's connection is hung writing for some reason,
// it's possible the acknowledgement queue fills up,
// in which case it drops the ACK and logs the issue.
func (c *Client) sendAck(mid messageID) {
	select {
	case c.ackQueue <- mid:
		c.logger.Println("KeepAlive received.")
	default:
		c.logger.Println("Discarding KeepAliveAck as queue is full. " +
			"This may indicate the Client's write side is broken " +
			"yet not timing out.")
	}
}

type ackHandler struct{}

func (ackHandler) handleMessage(c *Client, msg Message) {
	c.sendAck(msg.id)
}

func (c *Client) passToHandler(hdr Header) error {
	c.logger.Printf("finding handler for mID %d: %v", hdr.id, hdr.typ)

	handler := c.handlers[hdr.typ]

	c.awaitMu.Lock()
	replyChan, needsReply := c.awaiting[hdr.id]
	delete(c.awaiting, hdr.id)
	c.awaitMu.Unlock()

	payload := io.LimitReader(c.conn, int64(hdr.payloadLen))

	if needsReply {
		// TODO: this is mostly tech-debt from an earlier implementation; needs some refactoring
		if hdr.payloadLen > MaxBufferedPayloadSz {
			replyChan <- Message{Header: hdr} // SendMessage will reject it
		} else {
			buffResponse := make([]byte, hdr.payloadLen)
			if _, err := io.ReadFull(c.conn, buffResponse); err != nil {
				return nil
			}

			payload = bytes.NewBuffer(buffResponse)
			replyChan <- Message{Header: hdr, payload: bytes.NewBuffer(buffResponse)}
		}

		close(replyChan)
	}

	defer func() {
		_, err := io.Copy(ioutil.Discard, payload)
		err = errors.Wrapf(err, "failed to discard payload for %v", hdr)
	}()

	if handler != nil {
		c.handleGuarded(handler, Message{Header: hdr, payload: payload})
	} else if c.defaultHandler != nil {
		c.handleGuarded(c.defaultHandler, Message{Header: hdr, payload: payload})
	}

	return nil
}

func (c *Client) handleGuarded(handler MessageHandler, msg Message) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Printf("error: recovered from panic while after calling handler for %v: %v", msg, r)
		}
	}()

	if s, ok := handler.(fmt.Stringer); ok {
		c.logger.Printf("passing %v to handler %s", msg, s)
	} else {
		c.logger.Printf("passing %v to handler %T", msg, handler)
	}

	handler.handleMessage(c, msg)
}

// roHandler is a default handler implementation for ROAccessReports.
// It just unmarshals and logs the report.
type roHandler struct{}

func (roHandler) handleMessage(c *Client, m Message) {
	roar := &ROAccessReport{}
	buff, err := m.data()
	if err != nil {
		c.logger.Printf("error: %+v\n%# 02x", err, buff)
		return
	}

	if err := roar.UnmarshalBinary(buff); err != nil {
		c.logger.Printf("error: %+v\n%# 02x", err, buff)
		return
	}

	j, err := json.MarshalIndent(roar, "", "\t")
	if err != nil {
		c.logger.Printf("error: %+v\n%s", err, j)
		return
	}

	c.logger.Printf("%s", j)
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
// The client must be connected and serving the incoming/outgoing queues.
//
// It is the caller's responsibility to read the data or close the response.
// If the caller is slow or may panic, it should buffer its data.
// Canceling the context will stop processing at the earliest safe point,
// but if you're already up for writing, you must complete your outgoing message;
// likewise, once you receive a response, you must read or close it.
//
// If you cancel the context successfully, you'll receive (nil, ctx.Err()).
// If the connection closes while you're waiting to send or awaiting the reply,
// you'll receive an error wrapping ErrClientClosed.
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
func (c *Client) send(ctx context.Context, m Message) (Message, error) {
	// The write coordinator sends us a token to read or cancel the reply.
	// We shouldn't close this channel once the request is accepted.
	tokenChan := make(chan sendToken, 1)
	req := request{msg: m, tokenChan: tokenChan}

	// Wait until the message is sent, unless the Client is closed.
	select {
	case <-c.done:
		close(tokenChan)
		return Message{}, errors.Wrap(ErrClientClosed, "message not sent")
	case <-ctx.Done():
		close(tokenChan)
		return Message{}, ctx.Err()
	case c.sendQueue <- req:
		// The message was accepted; we can no longer cancel sending.
	}

	token := <-tokenChan // This should complete basically immediately.

	// Now we wait for the reply.
	select {
	case <-c.done:
		token.cancel()
		return Message{}, errors.Wrap(ErrClientClosed, "message sent, but not awaited")
	case <-ctx.Done():
		token.cancel()
		return Message{}, ctx.Err()
	case resp := <-token.replyChan:
		return resp, nil
	}
}

func (c *Client) checkInitialMessage() error {
	m, err := c.nextMessage()
	if err != nil {
		return err
	}
	defer m.Close()

	c.logger.Printf(">>> (initial) %v", m)
	if m.typ != MsgReaderEventNotification {
		return errors.Errorf("expected %v, but got %v", MsgReaderEventNotification, m.typ)
	}

	buf := make([]byte, m.payloadLen)
	if _, err := io.ReadFull(m.payload, buf); err != nil {
		return errors.Wrap(err, "failed to read message payload")
	}

	ren := ReaderEventNotification{}
	if err := ren.UnmarshalBinary(buf); err != nil {
		return errors.Wrap(err, "failed to unmarshal ReaderEventNotification")
	}

	if j, err := json.MarshalIndent(ren, "", "\t"); err == nil {
		c.logger.Printf(">>> (initial) %s", j)
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
func (c *Client) getSupportedVersion(ctx context.Context) (*GetSupportedVersionResponse, error) {
	resp, err := c.send(ctx, NewHdrOnlyMsg(MsgGetSupportedVersion))
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
	sv := GetSupportedVersionResponse{
		CurrentVersion:      Version1_0_1,
		MaxSupportedVersion: Version1_0_1,
	}

	// Every response message that includes an LLRPStatus parameter
	// puts the status first _except_ GetSupportedVersionResponse,
	// for some silly reason.
	// As a result, we have to check the type to know what parts to decode.
	switch resp.typ {
	default:
		return nil, errors.Errorf("unexpected response to %v: %v", MsgGetSupportedVersion, resp)
	case MsgErrorMessage:
		errMsg := ErrorMessage{}
		// If the reader only supports v1.0.1, it returns VersionUnsupported.
		// In that case, we'll drop the error so we can treat all results identically.
		if err := errMsg.UnmarshalBinary(data); err != nil {
			return nil, err
		}

		sv.LLRPStatus = errMsg.LLRPStatus

		if sv.LLRPStatus.Status == StatusMsgVerUnsupported {
			sv.LLRPStatus = LLRPStatus{Status: StatusSuccess}
		}

	case MsgGetSupportedVersionResponse:
		if err := sv.UnmarshalBinary(data); err != nil {
			return nil, err
		}
	}

	return &sv, errors.WithMessagef(sv.LLRPStatus.Err(), "%v returned an error", resp)
}

// negotiate LLRP versions with the RFID device.
//
// Upon success, this sets the Client's version to match the negotiated value.
// The Client will use the lesser of its configured version and the device's version,
// or LLRP v1.0.1 if the device does not support version negotiation.
// By default, newly created Clients use the max version supported by this package.
func (c *Client) negotiate() error {
	ctx := context.Background()
	if c.timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	sv, err := c.getSupportedVersion(ctx)
	if err != nil {
		return err
	}

	if sv.CurrentVersion == c.version {
		c.logger.Printf("device version matches Client: %v", c.version)
		return nil
	}

	ver := c.version
	if c.version > sv.MaxSupportedVersion {
		ver = sv.MaxSupportedVersion
		c.logger.Printf("downgrading Client version to match device max: %v", ver)
		c.version = ver

		// if the device is already using this, no need to set it
		if sv.CurrentVersion == c.version {
			return nil
		}
	}

	c.logger.Printf("requesting device use version %v", ver)

	m, err := NewByteMessage(MsgSetProtocolVersion, []byte{uint8(c.version)})
	if err != nil {
		return err
	}

	ctx = context.Background()
	if c.timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	resp, err := c.send(ctx, m)
	if err != nil {
		return err
	}
	defer resp.Close()

	if err := resp.isResponseTo(MsgSetProtocolVersion); err != nil {
		return err
	}

	return resp.Close()
}
