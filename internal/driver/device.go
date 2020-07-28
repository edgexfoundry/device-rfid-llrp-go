//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"encoding/json"
	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	"github.com/pkg/errors"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/retry"
	"net"
	"sync"
	"time"
)

const (
	shutdownGrace = time.Second // time permitted to Shutdown; if exceeded, Close is called
	maxRetries    = 3           // number of times to retry sending a message if it fails due to a closed reader

	dialTimeout  = time.Second * 10
	connTimeout  = time.Second * 30
	sendTimeout  = time.Second * 30
	noMsgTimeout = time.Second * 120 // resets the connection if more than 120s pass without a message.
)

// LLRPDevice represents a managed connection to a device that speaks LLRP.
// It is safe to use an LLRPDevice's methods concurrently.
//
// It self-manages its connection, redialing and reconnecting if it drops.
// It'll continue to do so until you tell it to Stop,
// after which it no longer receives reports/event notifications,
// and all TrySend attempts will fail.
//
// The device notifies the driver
// of incoming ROAccessReports & ReaderEventNotifications.
// It does some processing on incoming reports and events
// to make them more useful to down-the-line consumers.
type LLRPDevice struct {
	// Although the driver package can access these directly,
	// it's important to use the methods instead
	// to ensure it can properly handle connect/reconnect behavior.

	name string // comes from EdgeX
	lc   logger.LoggingClient

	addrMu  sync.RWMutex
	address net.Addr

	clientLock sync.RWMutex // we'll recreate client if it closes
	client     *llrp.Client
	cancel     context.CancelFunc // stops the reconnect process
}

// NewLLRPDevice returns an LLRPDevice which attempts to connect to the given address.
//
// It will continue to maintain this connection until Stop is called
// and will forward (semi)-processed ROAccessReports and ReaderEventNotifications
// to the driver.
func (d *Driver) NewLLRPDevice(name string, address net.Addr) *LLRPDevice {
	// We need a context to manage cancellation in some of the methods below,
	// and as a bonus, we can use it to simplify Stopping reattempts
	// when the driver shuts down.
	ctx, cancel := context.WithCancel(context.Background())
	// don't defer cancel() here; we only cancel() when Stop() is called.

	l := &LLRPDevice{
		name:    name,
		cancel:  cancel,
		address: address,
		lc:      d.lc,
	}

	// These options will be used each time we reconnect.
	opts := []llrp.ClientOpt{
		llrp.WithLogger(&edgexLLRPClientLogger{devName: name, lc: d.lc}),
		llrp.WithMessageHandler(llrp.MsgROAccessReport, newROHandler(l, d)),
		llrp.WithMessageHandler(llrp.MsgReaderEventNotification, newReaderEventHandler(l, d)),
		llrp.WithTimeout(noMsgTimeout),
	}

	// Create the initial client, which we can immediately make Send requests to,
	// though they can't be processed until it successfully connects.
	l.client = llrp.NewClient(opts...)
	c := l.client

	// This is all captured in a context to avoid exterior race conditions.
	go func() {
		dialer := net.Dialer{}

		// Until the context is canceled, attempt to dial and connect.
		// First establish a successful connection by dialing the address.
		// Each time dialing fails, back off a bit (up to a maximum).
		// Once we dial successfully, let the Client attempt to use it.
		// If the Client connection closes with a failure,
		// backoff the connection step but start again.
		// If the Client closes in a "normal" way while the context is still alive,
		// reset both retry/backoff policies and restart the dial/connect loop.
		for ctx.Err() == nil {
			d.lc.Debug("Establishing a new connection.", "device", name)

			// The error that would be returned can only be the result of canceling the context
			_ = retry.Quick.RetryWithCtx(ctx, retry.Forever, func(ctx context.Context) (bool, error) {
				// generate a new context
				connCtx, connCtxCancel := context.WithTimeout(ctx, connTimeout)
				defer connCtxCancel()

				var conn net.Conn
				if err := retry.Slow.RetryWithCtx(connCtx, retry.Forever, func(connCtx context.Context) (again bool, err error) {
					dialCtx, dialCtxCancel := context.WithTimeout(connCtx, dialTimeout)
					defer dialCtxCancel()

					l.addrMu.RLock()
					addr := l.address
					l.addrMu.RUnlock()

					d.lc.Debug("Attempting to dial Reader.", "address", addr.String(), "device", name)

					conn, err = dialer.DialContext(dialCtx, addr.Network(), addr.String())
					if err != nil {
						d.lc.Error("Failed to dial Reader.",
							"error", err.Error(),
							"address", addr.String(),
							"device", name)
					}
					return true, err
				}); err != nil {
					return true, err
				}

				defer conn.Close()

				d.lc.Debug("Attempting LLRP Client connection.", "device", name)

				// Block until the Client closes.
				clientErr := c.Connect(conn)
				_ = conn.Close()

				if errors.Is(clientErr, llrp.ErrClientClosed) {
					d.lc.Debug("LLRP Client connection closed normally.", "device", name)
					clientErr = nil // allow the backoff/retry policy to reset
				} else {
					d.lc.Error("Client connection closed unexpectedly.",
						"error", clientErr.Error(), "device", name)
				}

				// Replace the client, but don't start it until the next time we're connected.
				// Doing so allows new Send requests to wait until the connection opens.
				c = llrp.NewClient(opts...)
				l.clientLock.Lock()
				l.client = c
				l.clientLock.Unlock()

				return true, clientErr
			})
		}

		d.lc.Debug("No longer attempting to maintain active connection to device.", "device", name)
	}()

	return l
}

// TrySend works like llrp.SendFor,
// but will reattempt the send a few times if it fails due to a closed reader.
func (l *LLRPDevice) TrySend(ctx context.Context, request llrp.Outgoing, reply llrp.Incoming) error {
	return retry.Quick.RetryWithCtx(ctx, maxRetries, func(ctx context.Context) (bool, error) {
		l.lc.Debug("Attempting send.", "device", l.name, "message", request.Type().String())

		l.clientLock.RLock()
		c := l.client
		l.clientLock.RUnlock()
		if c == nil {
			return true, errors.New("no client available")
		}

		err := c.SendFor(ctx, request, reply)
		return err != nil && errors.Is(err, llrp.ErrClientClosed), err
	})
}

// Stop closes any open client connection and stops trying to reconnect.
//
// If the context is not canceled or past its deadline,
// it'll attempt a graceful shutdown.
// Otherwise/if the context is canceled/times out before completion,
// it forcefully closes the connection.
func (l *LLRPDevice) Stop(ctx context.Context) error {
	l.clientLock.Lock()
	defer l.clientLock.Unlock()

	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}

	return l.closeLocked(ctx)
}

// UpdateAddr updates the device address.
//
// If the device were Stopped, this won't start it, and this has no practical effect.
// It may return an error if closing the current connection fails for some reason.
// Nevertheless, it updates the address and will attempt to use it the next time it connects.
func (l *LLRPDevice) UpdateAddr(ctx context.Context, addr net.Addr) error {
	l.addrMu.Lock()
	l.address = addr
	l.addrMu.Unlock()

	l.clientLock.Lock()
	defer l.clientLock.Unlock()
	return l.closeLocked(ctx)
}

// closeLocked closes the current Client connection,
// but doesn't cancel it's context, so it'll restart on the next round.
// You must be holding the lock when you call this.
func (l *LLRPDevice) closeLocked(ctx context.Context) error {
	if l.client == nil {
		return nil
	}

	var err error
	if ctx.Err() == nil { // if the context isn't canceled/past its deadline, try Shutdown
		err = l.client.Shutdown(ctx)
		if err != nil && !errors.Is(err, llrp.ErrClientClosed) {
			_ = l.client.Close()
			err = errors.Wrap(err, "failed to shutdown gracefully")
		}
	} else { // otherwise, force close
		err = l.client.Close()
	}

	if err != nil && !errors.Is(err, llrp.ErrClientClosed) {
		return err
	}

	l.client = nil
	return nil
}

// newReaderEventHandler returns an llrp.MessageHandler bound to the l and d instances
// that converts the notification to an EdgeX event and sends it on d's asyncCh.
func newReaderEventHandler(l *LLRPDevice, d *Driver) llrp.MessageHandler {
	return llrp.MessageHandlerFunc(func(c *llrp.Client, msg llrp.Message) {
		event := &llrp.ReaderEventNotification{}

		// at some point, it may be worthwhile to provide a direct bin -> JSON conversion.
		if err := msg.UnmarshalTo(event); err != nil {
			d.lc.Error("failed to unmarshal LLRP reader event notification", "error", err.Error())
			return
		}

		data, err := json.Marshal(event)
		if err != nil {
			d.lc.Error("failed to marshal reader event notification to JSON", "error", err.Error())
			return
		}

		cv := dsModels.NewStringValue(ResourceReaderNotification, time.Now().UnixNano(), string(data))

		d.asyncCh <- &dsModels.AsyncValues{
			DeviceName:    l.name,
			CommandValues: []*dsModels.CommandValue{cv},
		}
	})
}

// newROHandler returns an llrp.MessageHandler bound to the l and d instances
// that processes the report to one or more EdgeX events and sends them on d's asyncCh.
func newROHandler(l *LLRPDevice, d *Driver) llrp.MessageHandler {
	return llrp.MessageHandlerFunc(func(c *llrp.Client, msg llrp.Message) {
		report := &llrp.ROAccessReport{}

		if err := msg.UnmarshalTo(report); err != nil {
			d.lc.Error("failed to unmarshal async event from LLRP", "error", err.Error())
			return
		}

		// todo: here we can add custom logic when processing tag report data.
		//  We should populate some fields on the LLRPDevice when we initially connect.
		//  Namely, it should grab device capabilities, config, & ro specs,
		//  since they are needed to disambiguate nils and timestamps within a report.
		//  If the reader doesn't have a UTC clock,
		//  we'll want to store our own UTC time + the connect's Uptime value.
		//  Then we can convert report/notification Uptimes to UTC by:
		//      reportUTC := savedUTC + (reportUptime - savedUptime)

		data, err := json.Marshal(report)
		if err != nil {
			d.lc.Error("failed to marshal async event to JSON", "error", err.Error())
			return
		}

		cv := dsModels.NewStringValue(ResourceROAccessReport, time.Now().UnixNano(), string(data))

		d.asyncCh <- &dsModels.AsyncValues{
			DeviceName:    l.name,
			CommandValues: []*dsModels.CommandValue{cv},
		}
	})
}
