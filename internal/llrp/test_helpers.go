//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// GetFunctionalClient attempts to dial and connect to an LLRP Reader at the given address.
//
// If it's unable to connect to the address, it fails the test immediately.
// It registers a Cleanup function to close the connection and checks for errors,
// and will run automatically when the test completes.
func GetFunctionalClient(t *testing.T, readerAddr string) (r *Client) {
	conn, err := net.Dial("tcp", readerAddr)
	if err != nil {
		t.Fatal(err)
	}

	opts := []ClientOpt{WithTimeout(300 * time.Second)}
	if testing.Short() {
		opts[0] = WithTimeout(10 * time.Second)
	}
	if testing.Verbose() {
		opts = append(opts, WithStdLogger("test"))
	}

	r = NewClient(opts...)

	errs := make(chan error)
	go func() {
		defer close(errs)
		errs <- r.Connect(conn)
	}()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
		defer cancel()
		if err := r.Shutdown(ctx); err != nil {
			if err := r.Close(); err != nil {
				if !errors.Is(err, ErrClientClosed) {
					t.Errorf("%+v", err)
				}
			}

			if !errors.Is(err, ErrClientClosed) {
				t.Errorf("%+v", err)
			}
		}

		for err := range errs {
			if !errors.Is(err, ErrClientClosed) {
				t.Errorf("%+v", err)
			}
		}
	})

	return r
}

// TestDevice is a useful mock of an LLRP device.
//
// Create one view NewTestDevice,
// then start it with ImpersonateReader,
type TestDevice struct {
	Client, reader *Client
	cConn, rConn   net.Conn

	ReaderLogs ClientLogger
	w          *msgWriter
	maxVer     VersionNum
	mid        messageID

	errsMu sync.Mutex
	errors []error
}

// NewTestDevice returns a TestDevice with a client ready to connect.
func NewTestDevice(maxReaderVer, maxClientVer VersionNum, timeout time.Duration, silent bool) (*TestDevice, error) {
	cConn, rConn := net.Pipe()

	logOpt := WithStdLogger("test")
	if silent {
		logOpt = WithLogger(nil)
	}

	client := NewClient(WithVersion(maxClientVer), WithTimeout(timeout), logOpt)
	reader := NewClient(WithVersion(Version1_0_1), logOpt)
	reader.conn = rConn

	td := TestDevice{
		Client: client,
		cConn:  cConn,
		rConn:  rConn,
		reader: reader,
		maxVer: maxReaderVer,
		w:      newMsgWriter(rConn, Version1_0_1),
	}

	reader.handlers[MsgGetSupportedVersion] = MessageHandlerFunc(td.getSupportedVersion)
	reader.handlers[MsgSetProtocolVersion] = MessageHandlerFunc(td.setVersion)
	reader.handlers[MsgCloseConnection] = MessageHandlerFunc(td.closeConnection)
	reader.defaultHandler = MessageHandlerFunc(td.handleUnknownMessage)

	return &td, nil
}

func (td *TestDevice) SetResponse(mt MessageType, out Outgoing) {
	td.reader.handlers[mt] = MessageHandlerFunc(func(_ *Client, msg Message) {
		if td.wrongVersion(msg) {
			return
		}
		td.write(msg.id, out)
	})
}

// Errors returns accumulated errors.
// It should only be called after the TestDevice is closed.
func (td *TestDevice) Errors() []error {
	return td.errors
}

func (td *TestDevice) Version() VersionNum {
	return td.w.header.version
}

// errCheck appends non-nil errors to its errors list and returns true.
// If err is nil, it simply returns false.
// It is safe for concurrent use.
func (td *TestDevice) errCheck(err error) bool {
	if err != nil {
		td.errsMu.Lock()
		td.errors = append(td.errors, err)
		td.errsMu.Unlock()
	}
	return err != nil
}

// wrongVersion checks if a message has a version different from the TestDevice.
// If so, it writes an LLRPStatus with the correct error and returns true.
// Otherwise, it returns false.
// Use it to quickly check the input and bail on any mismatched versions.
func (td *TestDevice) wrongVersion(msg Message) bool {
	if td.Version() == msg.version {
		return false
	}

	_ = td.errCheck(td.w.Write(msg.id, &ErrorMessage{LLRPStatus: LLRPStatus{
		Status:           StatusMsgVerUnsupported,
		ErrorDescription: StatusMsgVerUnsupported.defaultText(),
	}}))
	return true
}

// getSupportedVersion responds to the GetSupportedVersion message.
func (td *TestDevice) getSupportedVersion(_ *Client, msg Message) {
	if td.wrongVersion(msg) {
		return
	}

	rsp := &GetSupportedVersionResponse{
		CurrentVersion:      td.Version(),
		MaxSupportedVersion: td.maxVer,
	}

	// LLRP requires this message sent with the header's Version set to 1.1
	oldV := td.Version()
	defer func() { td.w.header.version = oldV }()

	td.w.header.version = Version1_1
	_ = td.errCheck(td.w.Write(msg.id, rsp))
}

// setVersion responds to the SetProtocolVersion message.
func (td *TestDevice) setVersion(_ *Client, msg Message) {
	if td.Version() == Version1_0_1 {
		_ = td.wrongVersion(msg)
		return
	}

	spv := SetProtocolVersion{}
	if td.errCheck(msg.UnmarshalTo(&spv)) {
		return
	}

	if spv.TargetVersion > td.maxVer {
		_ = td.errCheck(td.w.Write(msg.id, &ErrorMessage{LLRPStatus: LLRPStatus{
			Status:           StatusMsgVerUnsupported,
			ErrorDescription: fmt.Sprintf("max supported is %d", td.maxVer),
		}}))
		return
	}

	if spv.TargetVersion < VersionMin {
		_ = td.errCheck(td.w.Write(msg.id, &ErrorMessage{LLRPStatus: LLRPStatus{
			Status:           StatusMsgVerUnsupported,
			ErrorDescription: fmt.Sprintf("min supported is %d", VersionMin),
		}}))
		return
	}

	td.w.header.version = spv.TargetVersion
	// Setting the "reader" Client version races with the incoming message handler,
	// but it's only purpose here is to quite a warning log message.
	td.reader.version = spv.TargetVersion
	td.write(msg.id, &SetProtocolVersionResponse{})
}

// write a given Outgoing message with the given id.
func (td *TestDevice) write(mid messageID, out Outgoing) {
	_ = td.errCheck(td.w.Write(mid, out))
}

// handleUnknownMessage responds with the correct LLRPStatus for unknown messages.
func (td *TestDevice) handleUnknownMessage(_ *Client, msg Message) {
	if td.wrongVersion(msg) {
		return
	}

	td.write(msg.id, &ErrorMessage{LLRPStatus: LLRPStatus{
		Status:           StatusMsgMsgUnexpected,
		ErrorDescription: StatusMsgMsgUnsupported.defaultText(),
	}})
}

// ImpersonateReader prepares the TestDevice to impersonate an LLRP reader,
// as if a Client had correctly dialed it, but before version negotiation begins.
func (td *TestDevice) ImpersonateReader() {
	td.write(
		messageID(atomic.AddUint32((*uint32)(&td.mid), 1)),
		NewConnectMessage(ConnSuccess))
	close(td.reader.ready)
	// This is notably simpler than actually correctly managing the message queues.
	// All outgoing messages must be sent via the TestDevice's writer,
	// which IS NOT safe for concurrent use.
	td.errCheck(td.reader.handleIncoming())
}

// Close the Reader (RFID device) by attempting to send CloseMessage,
// then close net.Conn, returning any error from it.
// This is an abnormal close condition in LLRP.
func (td *TestDevice) Close() (err error) {
	defer func() {
		closeErr := td.reader.Close()
		if err == nil {
			err = closeErr
		}
	}()

	err = td.w.Write(
		messageID(atomic.AddUint32((*uint32)(&td.mid), 1)),
		NewConnectMessage(ConnSuccess))
	return
}

// closeConnection handles a client request to close the connection.
func (td *TestDevice) closeConnection(_ *Client, msg Message) {
	if td.wrongVersion(msg) {
		return
	}

	td.write(msg.id, &CloseConnectionResponse{})
}

// ConnectClient correctly connects the Client to the TestDevice and returns it.
// It registers a Cleanup function to Shutdown the Client and report errors
// once the test is completed, so it is not necessary to do so yourself.
func (td *TestDevice) ConnectClient(t *testing.T) (c *Client) {
	c = td.Client

	connErrs := make(chan error)
	go func() {
		defer close(connErrs)
		connErrs <- c.Connect(td.cConn)
	}()

	t.Cleanup(func() {
		if err := td.Close(); err != nil {
			t.Errorf("%+v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
		defer cancel()
		if err := td.Client.Shutdown(ctx); err != nil {
			if err := td.Client.Close(); err != nil {
				t.Errorf("%+v", err)
			}
			t.Errorf("%+v", err)
		}

		for err := range connErrs {
			if !errors.Is(err, ErrClientClosed) {
				t.Errorf("%+v", err)
			}
		}
	})

	return c
}

func NewConnectMessage(eventType ConnectionAttemptEventType) *ReaderEventNotification {
	c := ConnectionAttemptEvent(eventType)
	return &ReaderEventNotification{
		ReaderEventNotificationData: ReaderEventNotificationData{
			UTCTimestamp:           UTCTimestamp(time.Now().UnixNano() / 1000),
			ConnectionAttemptEvent: &c,
		}}
}

func NewCloseMessage() *ReaderEventNotification {
	return &ReaderEventNotification{
		ReaderEventNotificationData: ReaderEventNotificationData{
			UTCTimestamp:         UTCTimestamp(time.Now().UnixNano() / 1000),
			ConnectionCloseEvent: &ConnectionCloseEvent{},
		}}
}
