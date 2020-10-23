//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"
)

// TestEmulator acts like an LLRP server. It accepts connections on a configurable network port,
// and when an LLRP client dials this TestEmulator, a new TestDevice is created which manages only
// the "reader" side of the connection.
//
// Functions exist to allow developer to provide "canned" responses to messages, and change them on the fly
// to allow changing
//
// NOTE: Unlike an actual LLRP server/reader, more than one simultaneous client are allowed to connect to it,
//		 however each client will receive the same canned responses.
type TestEmulator struct {
	silent bool

	listener net.Listener
	done     uint32

	// keep track of canned responses
	responses   map[MessageType]Outgoing
	responsesMu sync.Mutex

	// active connections/devices
	devices   map[*TestDevice]bool
	devicesMu sync.Mutex
}

func NewTestEmulator(silent bool) *TestEmulator {
	return &TestEmulator{
		silent:    silent,
		responses: make(map[MessageType]Outgoing),
		devices:   make(map[*TestDevice]bool),
	}
}

// StartAsync starts listening on a specific port until emu.Shutdown() is called
func (emu *TestEmulator) StartAsync(port int) error {
	var err error

	emu.listener, err = net.Listen("tcp4", ":"+strconv.Itoa(port))
	if err != nil {
		return err
	}

	go emu.listenUntilCancelled()
	return nil
}

// Shutdown attempts to cleanly shutdown the emulator
func (emu *TestEmulator) Shutdown() error {
	atomic.StoreUint32(&emu.done, 1)
	if err := emu.listener.Close(); err != nil {
		return err
	}

	emu.devicesMu.Lock()
	defer emu.devicesMu.Unlock()

	for dev := range emu.devices {
		if err := dev.Close(); err != nil {
			return err
		}
	}

	return nil
}

// listenUntilCancelled listens forever on the net.Listener until emu.Shutdown() is called
func (emu *TestEmulator) listenUntilCancelled() {
	for {
		conn, err := emu.listener.Accept()
		if atomic.LoadUint32(&emu.done) == 1 {
			return
		} else if err != nil {
			panic("listener accept failed: " + err.Error())
		}

		go emu.handleNewConn(conn)
	}
}

// handleNewConn handles a new incoming connection the net.Listener (should be spawned async)
func (emu *TestEmulator) handleNewConn(conn net.Conn) {
	td, err := NewReaderOnlyTestDevice(conn, emu.silent)
	if err != nil {
		panic("unable to create a new ReaderOnly TestDevice: " + err.Error())
	}

	emu.responsesMu.Lock()
	for mt, out := range emu.responses {
		td.SetResponse(mt, out)
	}
	emu.responsesMu.Unlock()
	td.reader.handlers[MsgCloseConnection] = emu.createCloseHandler(td)

	emu.devicesMu.Lock()
	emu.devices[td] = true
	emu.devicesMu.Unlock()

	td.ImpersonateReader()
}

// SetResponse adds a canned response to all future clients. Optionally,
// if `applyExisting` is true, this will affect all currently active clients.
//
// NOTE 1: This will OVERRIDE existing response for given message type
//
// NOTE 2: Setting the response for MsgCloseConnection is NOT SUPPORTED and will be overridden by internal handler.
func (emu *TestEmulator) SetResponse(mt MessageType, out Outgoing) {
	emu.responsesMu.Lock()
	emu.responses[mt] = out
	emu.responsesMu.Unlock()
}

// createCloseHandler handles a client request to close the connection.
func (emu *TestEmulator) createCloseHandler(td *TestDevice) MessageHandlerFunc {
	return func(_ *Client, msg Message) {
		if td.wrongVersion(msg) {
			return
		}

		td.write(msg.id, &CloseConnectionResponse{})

		_ = td.reader.Close()
		_ = td.rConn.Close()

		emu.devicesMu.Lock()
		// remove it from the set of active connections
		delete(emu.devices, td)
		emu.devicesMu.Unlock()
	}
}
