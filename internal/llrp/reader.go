//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

/*
Package llrp implements the Low Level Reader Protocol (LLRP)
to communicate with compliant RFID Readers.

This package focuses on Client-side communication,
and handles the protocol minutia like Keep-Alive acknowledgements
while providing types to facilitate handling messages you care about.

A typical use of this package focuses on the Client connection:
- Create a new Client with connection details and message handlers.
- Establish and maintain an LLRP connection with an RFID device.
- Send and receive LLRP messages.
- At some point, gracefully close the connection.

The package provides methods that parse and validate
LLRP message to translate them among binary, Go types, and JSON.

Note that names in LLRP are often verbose and sometimes overloaded.
These names have been judiciously translated when appropriate
to better match Go idioms and standards.

------------------------------------------

An LLRP message consists of a header identifying its type and size,
followed by 0 or more data fields.
Some of these fields have a fixed size and offset (relative the header),
but others are optional and/or variable length,
so they start with a parameter header to identify their type and (usually) size.
In some cases, a parameter's header only identifies its type,
in which case its size must be determined by a lookup table.

Most LLRP Parameters are TLVs.
Their headers are 4 bytes:
6 reserved bits (zero), a 10 bit type, and 16 bit of length (incl. header).
The type field of a TLV is specified as 128-2047,
though it's not clear how types 1024-2047 fit in 10 bits.
TV parameters can (in theory) be as short as 1 byte.
The MSBit is always 1, then the next 7 are the type: 1-127
(so in a certain sense, they range 128-255).

Most parameter types are represented by Go structs,
with their LLRP fields and parameters given as struct fields.
As a special case, if the struct would consist of a single numeric field,
then its type is defined as "type <paramName> <backing type>".

Most fields are represented by an int/uint of the appropriate storage size,
or as an enumeration, flag, or type alias backed such.
Variable length fields are represented by slices of the above,
with the following exceptions:
  - LLRP strings are represented by Go strings;
    the bytes are taken as-is and not validated for UTF-8 correctness.
  - In Custom params/messages, all non-header bytes are stored as "Data []byte".
  - Bit arrays keep a "<name>NumBits uint16" for their bit length
    and store the bit data in a "<name> []byte" field
    with the same MSB offset and octet padding as the original message value.

Optional parameters are represented by pointer values
and are left nil if not present during unmarshaling.
Repeatable parameters are represented by slices,
which may be nil if no parameter of the given type is present.
Required, non-repeatable parameters are regular struct fields.
*/
package llrp

import (
	"bytes"
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
	"time"
)

// Client represents a client connection to an LLRP-compatible RFID reader.
type Client struct {
	conn           net.Conn       // underlying network connection
	sendQueue      chan request   // controls write-side of connection
	ackQueue       chan messageID // gesundheit -- allows ACK'ing fast, unless sendQueue is unhealthy
	awaitMu        sync.Mutex     // synchronize awaiting map access
	awaiting       awaitMap       // message IDs -> awaiting reply
	logger         ClientLogger   // reports important Client events
	handlers       map[MessageType]MessageHandler
	defaultHandler MessageHandler // used if no MessageHandlers for type and nothing awaiting reply
	timeout        time.Duration  // if non-zero, causes updates to conn's deadline on each read/write
	done           chan struct{}  // closed when the Client is closed
	ready          chan struct{}  // closed when the connection is negotiated
	isClosed       uint32         // used atomically to prevent duplicate closure of done
	version        VersionNum     // sent in headers; established during negotiation
}

const (
	// VersionMin is the minimum LLRP version supported by this package.
	VersionMin = Version1_0_1

	// VersionMax is the maximum LLRP version supported by this package.
	// See WithVersion for more information.
	VersionMax = Version1_1

	// MaxBufferedPayloadSz is the maximum payload size permitted in responses.
	//
	// Although LLRP theoretically can handle message up to 4GiB,
	// in practice most messages are nowhere near this size.
	// One large manufacturer even states explicitly that
	// they'll simply close the connection if a message exceeds ~500KiB,
	// including outgoing tag reports.
	//
	// MessageHandlers aren't restricted to this limitation,
	// but as a result, require processing the message synchronously,
	// as the incoming message must be read in full (even if discarded)
	// before any other message can be read.
	MaxBufferedPayloadSz = uint32((1 << 10) * 640)
)

// NewClient returns a Client configured by the given options.
//
// Without options, a Client gets a default logger prefixed with its Conn address,
// does not set deadlines on the connection,
// uses the package's max LLRP version to negotiate connections,
// and sends ACKs to KeepAlive messages from the reader.
func NewClient(opts ...ClientOpt) *Client {
	// ackQueueSz is arbitrary, but if it fills,
	// warnings are logged, as it likely indicates a Write problem.
	// At some point, we might consider resetting the connection.
	const ackQueueSz = 5

	c := &Client{
		version:   VersionMax,
		done:      make(chan struct{}),
		ready:     make(chan struct{}),
		sendQueue: make(chan request),
		ackQueue:  make(chan messageID, ackQueueSz),
		awaiting:  make(awaitMap),
		handlers: map[MessageType]MessageHandler{
			MsgKeepAlive: ackHandler{},
		},
	}

	for _, opt := range opts {
		opt.do(c)
	}

	return c
}

// ClientOpt modifies a Client during construction.
type ClientOpt interface {
	do(*Client) // don't allow arbitrary implementations for now
}

type clientOpt func(c *Client)

func (ro clientOpt) do(c *Client) {
	ro(c)
}

// WithVersion sets the max permitted LLRP version for this connection.
//
// The actual version used during communication
// is selected upon connection using LLRP's version negotiation messages.
// It is the higher of the value given here and that supported by the Reader.
// Note that if the version is to 1.0.1, version negotiation is skipped.
//
// This panics if given an unsupported version.
func WithVersion(v VersionNum) ClientOpt {
	if v < VersionMin || v > VersionMax {
		panic(errors.Errorf("unsupported version %v", v))
	}
	return clientOpt(func(c *Client) {
		c.version = v
	})
}

// WithTimeout sets timeout on the connection before each read and write.
//
// If non-zero, the client will call SetReadDeadline and SetWriteDeadline
// before reading or writing respectively.
//
// Note that if set, the connection will automatically close
// if the RFID device fails to send a message within the timeout,
// as no reads will have arrived before the deadline.
// As a result, this is most useful when combined with LLRP KeepAlive
// and set to an integer multiple of the KeepAlive interval plus a small grace.
// In that case, the client will automatically close the connection
// if it fails to receive some multiple of KeepAlive messages from the RFID device.
//
// This package does not automatically request the Reader enable KeepAlives,
// as their exact behavior within LLRP is technically implementation-defined
// (in practice, Reader manufacturers will almost certainly use them as described).
// Therefore, if you wish to enable KeepAlive messages,
// you should send a SetReaderConfiguration message.
func WithTimeout(d time.Duration) ClientOpt {
	if d < 0 {
		panic(errors.Errorf("timeout should be at least 0, but is %v", d))
	}
	return clientOpt(func(c *Client) {
		c.timeout = d
	})
}

// ClientLogger is used by the Client to notify the user of certain events.
// By default, new Clients log these message with the StdLogger,
// but that can be changed via WithLogger.
type ClientLogger interface {
	ReceivedMsg(Header, VersionNum) // called with the client's current version when it receives a message
	SendingMsg(Header)              // called just before writing a message to the connection
	MsgHandled(Header)              // called after a message is sent to a handler or awaiting reply listener
	MsgUnhandled(Header)            // called if a message is discarded because it had no handler or listener
	HandlerPanic(Header, error)     // called if a handler panics while handling a message
}

// WithStdLogger uses the Go stdlib Logger for Client events.
//
// This logger is used by default if the WithLogger option is not given,
// but if needed, this can be used to override an earlier WithLogger option
// (e.g., using a standard list of ClientOpts and overriding some elsewhere).
func WithStdLogger(prefix string) ClientOpt {
	return clientOpt(func(c *Client) {
		c.setStdLogger(prefix)
	})
}

// WithLogger sets a logger for the Client.
//
// By default, new clients get a simple logger that outputs messages
// prefixed with the client's name or address and a timestamp.
// If you don't want any log output, set this to nil.
func WithLogger(l ClientLogger) ClientOpt {
	if l == nil {
		l = devNullLog // this way we'll always have a valid pointer
	}

	return clientOpt(func(c *Client) {
		c.logger = l
	})
}

// MessageHandler can be implemented to handle certain messages received from the Reader.
// See Client.WithHandler for more information.
type MessageHandler interface {
	HandleMessage(c *Client, msg Message)
}

// MessageHandlerFunc can wrap a function so it can be used as a MessageHandler.
type MessageHandlerFunc func(c *Client, msg Message)

// HandleMessage implements MessageHandler for MessageHandlerFunc by calling the function.
func (mhf MessageHandlerFunc) HandleMessage(c *Client, msg Message) {
	mhf(c, msg)
}

// WithMessageHandler sets a handler which will be called
// whenever a message of the given type arrives.
//
// Handlers run in the same goroutine as the read side of the connection,
// blocking it until the handler completes.
// The message's payload reader is only valid while in that thread of execution;
// after the handler returns, any remaining unread payload is discarded.
// It is guarded from panics, but you should avoid any long-running operations.
// so if you want to start a goroutine to process the message,
// you'll need to copy it to a buffer within the main body of the handler.
//
// Each message type has only a single handler,
// so if you wish to support multiple behaviors for a single message type,
// you'll need to copy and propagate the message to the other handlers.
// Setting a handler for the same message more than once
// will overwrite previously recorded handlers.
// Setting it to nil will delete the handler for that type.
// If a message doesn't have a Handler, it may be sent to a DefaultHandler,
// if configured.
//
// Clients are created with a handler for KeepAlive (to send KeepAliveAck).
// If you override this, you'll need to acknowledge the KeepAlives yourself.
func WithMessageHandler(mt MessageType, handler MessageHandler) ClientOpt {
	if !mt.isValid() {
		panic(errors.Errorf("invalid message type for handler: %v", mt))
	}

	return clientOpt(func(c *Client) {
		if handler == nil {
			delete(c.handlers, mt)
		} else {
			c.handlers[mt] = handler
		}
	})
}

// WithDefaultHandler sets a handler for messages that aren't handled by
// either an awaiting listener or another MessageHandler.
//
// Only a single default handler is supported.
// It may be set to nil, in which case unhandled non-reply messages are simply dropped.
func WithDefaultHandler(handler MessageHandler) ClientOpt {
	return clientOpt(func(c *Client) {
		c.defaultHandler = handler
	})
}

// devNullLogger implements the ClientLogger interface, but discards everything.
type devNullLogger struct{}

// devNullLog is a single package instance of the devNullLogger.
// If the user sets the Client's logger to nil, we change it to devNullLogger.
// It's also used by tests when Verbose is false.
var devNullLog devNullLogger

func (devNullLogger) ReceivedMsg(Header, VersionNum) {}
func (devNullLogger) SendingMsg(Header)              {}
func (devNullLogger) MsgHandled(Header)              {}
func (devNullLogger) MsgUnhandled(Header)            {}
func (devNullLogger) HandlerPanic(Header, error)     {}

// StdLogger wraps the Go stdlib Logger.
type StdLogger struct {
	*log.Logger
}

// setStdLogger sets the logger to an instance of a Go stdlib logger,
// prefixed with a useful name.
func (c *Client) setStdLogger(prefix string) {
	l := &StdLogger{}
	l.Logger = log.New(os.Stderr, "LLRP-"+prefix+"-", log.LstdFlags)
	c.logger = l
}
func (l *StdLogger) SendingMsg(hdr Header) {
	l.Printf("<<< message{%v}", hdr)
}

func (l *StdLogger) ReceivedMsg(hdr Header, curVer VersionNum) {
	l.Printf(">>> message{%v}", hdr)
	if curVer != hdr.version {
		l.Printf("warning: incoming message version != %v: %v", curVer, hdr)
	}
}

func (l *StdLogger) MsgHandled(hdr Header) {
	l.Printf("handled message{%v}", hdr)
}

func (l *StdLogger) MsgUnhandled(hdr Header) {
	l.Printf("no handler for message{%v}", hdr)
}

func (l *StdLogger) HandlerPanic(hdr Header, err error) {
	l.Printf("error: recovered from panic while handling message{%v}: %v", hdr, err)
}

var (
	// ErrClientClosed is returned if an operation is attempted on a closed Client,
	// indicating that Shutdown or Close was called.
	// It may be wrapped, so to check for it, use errors.Is.
	ErrClientClosed = goErrs.New("client closed")
)

// Connect to an LLRP-capable device and start processing messages.
//
// This takes ownership of the connection and blocks,
// serving the connection's incoming and outgoing messages
// until either it encounters an error or the Client is closed
// via a call to either Shutdown or Close.
// It does not close the net.Conn parameter.
//
// It is NOT safe to call Connect more than once.
// Doing so has undefined results.
// Do not reuse a Client; create a new one.
// This will (likely) change in the future, but not yet.
//
// If the Client is closed via Shutdown or Close,
// this returns ErrClientClosed or an error wrapping it;
// otherwise, it returns the first error it encounters.
// You can use this as a signal to identify whether
// the communication on a particular connection was successful.
// If this returns ErrClientClosed, it indicates some other portion of your code
// actively requested the connection to close.
// Any other error indicates a failed communication attempt,
// either due to an issue reading/writing on the connection,
// or the connected device severing the connection unexpectedly.
func (c *Client) Connect(conn net.Conn) error {
	defer c.Close()
	if conn == nil {
		return errors.New("nil connection")
	}
	c.conn = conn

	// If the user doesn't set a logger, create a default one.
	// If they called WithLogger(nil), the logger is our devNullLogger,
	// so we always have a non-nil pointer.
	if c.logger == nil {
		c.setStdLogger(conn.RemoteAddr().String())
		defer func() { c.logger = nil }()
	}

	if err := c.checkInitialMessage(); err != nil {
		close(c.ready)
		return err
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		errs <- c.handleOutgoing()
	}()
	go func() {
		defer wg.Done()
		errs <- c.handleIncoming()
	}()

	if c.version > Version1_0_1 {
		if err := c.negotiate(); err != nil {
			return err
		}
	}

	// The `ready` channel gates external Send requests until after
	// the first few LLRP messages are taken care of by the Client.
	close(c.ready)

	var err error
	select {
	case err = <-errs:
		_ = c.Close()
	case <-c.done:
		err = ErrClientClosed
	}

	wg.Wait()

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
//
// It's safe to call Shutdown multiple times,
// but at most only one will return a nil error;
// others will return ErrClientClosed or an error wrapping it.
// if only a single code path can call Shutdown, if it returns an error,
// then Connect will also return an error that does NOT wrap ErrClientClosed.
func (c *Client) Shutdown(ctx context.Context) error {
	rTyp, resp, err := c.SendMessage(ctx, MsgCloseConnection, nil)
	if err != nil {
		return err
	}

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
		return errors.Wrap(ErrClientClosed, "attempt to close already closed connection")
	}
}

// SendFor sends an Outgoing message and expects the Incoming reply.
//
// This method round-trips a typical LLRP message exchange
// by marshaling the Outgoing message to LLRP binary,
// sending the message and awaiting the reply,
// and unmarshaling the result from LLRP binary to the Incoming struct.
//
// It is safe to call this method even if the Client isn't connected;
// it blocks until the message is sent and a reply received,
// unless the context is canceled.
//
// This method converts LLRP errors into *StatusErrors:
// if the Incoming message is an ErrorMessage,
// it unmarshals it and returns it as a *StatusError.
// If the Incoming message is Statusable (i.e., has an LLRPStatus),
// and that LLRPStatus is not Success, this converts it to a *StatusError.
//
// Even if this returns a *StatusError,
// Incoming may be fully or partially unmarshalled,
// which may give useful context to the particular LLRP error details.
// However, if the reply message type doesn't match Incoming's type,
// it won't be unmarshalled, and this will return an error.
func (c *Client) SendFor(ctx context.Context, out Outgoing, in Incoming) error {
	outData, err := out.MarshalBinary()
	if err != nil {
		return err
	}

	respT, respV, err := c.SendMessage(ctx, out.Type(), outData)
	if err != nil {
		return err
	}

	expT := in.Type()
	switch respT {
	case expT:
	case MsgErrorMessage:
		em := ErrorMessage{}
		if err := em.UnmarshalBinary(respV); err != nil {
			return errors.Wrapf(err,
				"expected message response %v, but got an error message; "+
					"however, it failed to unmarshal properly", expT)
		}
		return errors.Wrapf(em.LLRPStatus.Err(),
			"expected message response %v, but got an error message", expT)
	default:
		return errors.Errorf("expected message response %v, but got %v", expT, respT)
	}

	if err := in.UnmarshalBinary(respV); err != nil {
		return errors.Errorf("failed to unmarshal %v\nraw data: 0x%02x\npartial unmarshal:\n%+v",
			respT, respV, in)
	}

	if st, ok := in.(Statusable); ok {
		s := st.Status()
		return s.Err()
	}

	return nil
}

// SendMessage sends an arbitrary payload with a header matching the given MessageType.
// It awaits a response to the message, then buffers and returns it.
//
// This method can be called at any time, even before the Reader is connected,
// though if Closed is called before the message is sent,
// it will return an error wrapping ErrClientClosed.
// If the message does not require a payload,
// you may pass an empty or nil data buffer.
//
// Also see SendFor, which makes it easier to send specific LLRP messages
// and SendNoWait, which sends a message without expecting a response.
func (c *Client) SendMessage(ctx context.Context, typ MessageType, data []byte) (MessageType, []byte, error) {
	select {
	case <-c.ready: // ensure the connection is negotiated
	case <-c.done:
		return 0, nil, errors.Wrap(ErrClientClosed, "message not sent")
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}

	// It's possible one of the other two select channels is also ready,
	// but it'll they'll get checked again within send,
	// so adding another check here is just unnecessary expense
	// that would only avoid a rather cheap allocation.

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

// SendNoWait sends a message without awaiting the reply.
//
// It still blocks until the message is sent,
// which can only happen after the connection is open and negotiated
// and messages earlier in the queue are sent.
func (c *Client) SendNoWait(ctx context.Context, m Message) error {
	select {
	case <-c.ready: // ensure the connection is negotiated
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.Wrap(ErrClientClosed, "message not sent")
	}

	select {
	case <-c.done:
		return errors.Wrap(ErrClientClosed, "message not sent")
	case <-ctx.Done():
		return ctx.Err()
	case c.sendQueue <- request{msg: m}:
		return nil
	}
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
			return ErrClientClosed
		default:
		}

		hdr, err := c.readHeader()
		if err != nil {
			if !receivedClosed {
				return errors.Wrap(err, "failed to get next message")
			}

			if !(errors.Is(err, io.EOF) || os.IsTimeout(err) || errors.Is(err, io.ErrClosedPipe)) {
				return errors.Wrap(err, "timeout")
			}

			// EOF/io.Timeout after CloseConnection should wait for reader to close
			<-c.done
			return ErrClientClosed
		}

		c.logger.ReceivedMsg(hdr, c.version)

		if hdr.typ == MsgCloseConnectionResponse {
			receivedClosed = true
		}

		err = c.passToHandler(hdr)

		switch err {
		case nil:
		case io.EOF:
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
// If this sends MsgCloseConnection, it stops processing messages
// and waits for the Client to close.
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
			return ErrClientClosed
		case mid := <-c.ackQueue:
			msg = Message{Header: Header{id: mid, typ: MsgKeepAliveAck}}
		default:
			select {
			case <-c.done:
				return ErrClientClosed
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

		c.logger.SendingMsg(msg.Header)
		if err := c.writeHeader(msg.Header); err != nil {
			return err
		}

		// stop processing messages
		if msg.typ == MsgCloseConnection {
			// after CloseConnection, wait for reader to close
			<-c.done
			return ErrClientClosed
		}

		if msg.payloadLen == 0 {
			continue
		}

		if msg.payload == nil {
			return errors.Errorf("message data is nil, but has length >0 (%v)", msg)
		}

		// It assumes that msg.payload is cooperating and will return EOF
		// or another error and blocks until then.
		if n, err := io.Copy(c.conn, msg.payload); err != nil {
			return errors.Wrapf(err, "write failed after %d bytes for %v", n, msg)
		}
	}
}

// ackHandler acknowledges KeepAlive messages.
//
// If the Client's connection is hung writing for some reason,
// it's possible the acknowledgement queue fills up,
// in which case it drops the ACK and logs the issue.
type ackHandler struct{}

// HandleMessage handles KeepAlive messages using the ackHandler.
func (ackHandler) HandleMessage(c *Client, msg Message) {
	select {
	case c.ackQueue <- msg.id:
	default:
		panic("KeepAliveAck as queue is full!") // panic will be caught
	}
}

// passToHandler passes control of the connection's read side
// to the handler appropriate for the message indicated by the Header.
//
// Specifically, this means if something is awaiting a Reply,
// it will send a Message on the replyChan and close it.
// Usually, that Message includes the buffered payload,
// but if the Header indicates a gigantic message, it's empty instead.
//
// If there's a MessageHandler for the type indicated by the Header,
// or a default handler for otherwise unregistered types,
// it'll then get called with a new Message instance.
// If the payload is buffered already, we give it an io.Reader wrapping that;
// otherwise it's an io.LimitedReader with the connection and payload length.
// After the handler returns, that LimitedReader is drained to ioutil.Discard
// to ensure the full payload has been read from the net.Conn.
//
// Handlers are called via handleGuarded to protect against panics.
// A handler blocks reads from making progress.
func (c *Client) passToHandler(hdr Header) (err error) {
	handler := c.handlers[hdr.typ]

	c.awaitMu.Lock()
	replyChan, needsReply := c.awaiting[hdr.id]
	delete(c.awaiting, hdr.id)
	c.awaitMu.Unlock()

	if !needsReply && handler == nil && c.defaultHandler == nil {
		c.logger.MsgUnhandled(hdr)
		_, err = io.CopyN(ioutil.Discard, c.conn, int64(hdr.payloadLen))
		return errors.Wrapf(err, "failed to discard payload for %v", hdr)
	}

	payload := io.LimitReader(c.conn, int64(hdr.payloadLen))
	defer func() {
		c.logger.MsgHandled(hdr)
		_, err = io.Copy(ioutil.Discard, payload)
		err = errors.Wrapf(err, "failed to discard payload for %v", hdr)
	}()

	if needsReply {
		if hdr.payloadLen > MaxBufferedPayloadSz {
			replyChan <- Message{Header: hdr} // SendMessage will reject it
		} else {
			buffResponse := make([]byte, hdr.payloadLen)
			if _, err = io.ReadFull(c.conn, buffResponse); err != nil {
				return err
			}

			// This doesn't have to be a bytes.Buffer necessarily,
			// but it must implement Bytes() ([]byte, error)
			// so Message.data can distinguish buffered and unbuffered payloads.
			payload = bytes.NewBuffer(buffResponse)
			replyChan <- Message{Header: hdr, payload: bytes.NewBuffer(buffResponse)}
		}

		close(replyChan)
	}

	if handler != nil {
		c.handleGuarded(handler, Message{Header: hdr, payload: payload})
	} else if c.defaultHandler != nil {
		c.handleGuarded(c.defaultHandler, Message{Header: hdr, payload: payload})
	}

	return nil
}

// handleGuarded recovers from panic'ing MessageHandlers.
func (c *Client) handleGuarded(handler MessageHandler, msg Message) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}

		if err, ok := r.(error); ok {
			c.logger.HandlerPanic(msg.Header, err)
		} else {
			c.logger.HandlerPanic(msg.Header, errors.Errorf("handler panic: %v", r))
		}
	}()

	handler.HandleMessage(c, msg)
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

// checkInitialMessage reads the first message off the connection,
// which should be a ReaderEventNotification with a successful ConnectEvent.
// If it isn't, this returns an error.
//
// This skips the Client's send and
func (c *Client) checkInitialMessage() error {
	hdr, err := c.readHeader()
	if err != nil {
		return err
	}

	c.logger.ReceivedMsg(hdr, c.version)

	if hdr.payloadLen > MaxBufferedPayloadSz {
		return errors.Errorf("initial connection message has huge size; "+
			"it almost certainly not a valid ReaderEventNotification: %d "+
			"(note: max buffered payload size is %d)",
			hdr.payloadLen, MaxBufferedPayloadSz)
	}

	buf := make([]byte, hdr.payloadLen)
	if _, err := io.ReadFull(c.conn, buf); err != nil {
		return errors.Wrap(err, "failed to read message payload")
	}

	if h, ok := c.handlers[hdr.typ]; ok {
		c.handleGuarded(h, Message{Header: hdr, payload: bytes.NewBuffer(buf)})
	}

	if hdr.typ != MsgReaderEventNotification {
		return errors.Errorf("expected %v, but got %v", MsgReaderEventNotification, hdr.typ)
	}

	ren := ReaderEventNotification{}
	if err := ren.UnmarshalBinary(buf); err != nil {
		return errors.Wrap(err, "failed to unmarshal ReaderEventNotification")
	}

	connAttempt := ren.ReaderEventNotificationData.ConnectionAttemptEvent
	if connAttempt == nil {
		return errors.Errorf("initial reader event did not include connection attempt: %v", ren)
	}

	switch ConnectionAttemptEventType(*connAttempt) {
	case ConnSuccess:
		return nil
	case ConnExistsClientInitiated, ConnExistsReaderInitiated:
		return errors.New("reader is already connected to another client")
	case ConnAttemptedAgain:
		// This status should never occur as the first message.
		// If another Client is connected already,
		// that Client should see this event, not us.
		return errors.New("reader indicates we're already connected")
	}

	return errors.New("connection failed for unknown reasons")
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

	// Use the max of our desired version & the Reader's max supported version.
	if c.version > sv.MaxSupportedVersion {
		c.version = sv.MaxSupportedVersion
	}

	// If the device is already using this, no need to set it.
	if sv.CurrentVersion == c.version {
		return nil
	}

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

	spvResp := &SetProtocolVersionResponse{}
	if err := resp.UnmarshalTo(spvResp); err != nil {
		return err
	}

	return spvResp.LLRPStatus.Err()
}
