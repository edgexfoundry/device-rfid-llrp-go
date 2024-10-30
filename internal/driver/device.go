//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/edgexfoundry/device-sdk-go/v4/pkg/interfaces"
	dsModels "github.com/edgexfoundry/device-sdk-go/v4/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/v4/clients/logger"
	"github.com/edgexfoundry/go-mod-core-contracts/v4/common"
	contract "github.com/edgexfoundry/go-mod-core-contracts/v4/models"

	"github.com/edgexfoundry/device-rfid-llrp-go/internal/retry"
	"github.com/edgexfoundry/device-rfid-llrp-go/pkg/llrp"
)

const (
	dialTimeout       = time.Second * 30 // how long to wait after dialing for the Reader to answer
	sendTimeout       = time.Second * 20 // how long to wait in each send attempt in TrySend
	shutdownGrace     = time.Second      // time permitted to Shutdown; if exceeded, we call Close
	maxSendAttempts   = 3                // number of times to retry send in TrySend
	keepAliveInterval = time.Second * 30 // how often the Reader should send us a KeepAlive
	maxMissedKAs      = 2                // number of KAs that can be "missed" before resetting a connection
	maxConnAttempts   = 2                // number of times to retry connecting before considering the device offline
)

// LLRPDevice manages a connection to a device that speaks LLRP,
// and notifies EdgeX of relevant device events.
//
// LLRPDevice attempts to maintain a connection to the Reader,
// provides methods to retry message sends that fail due to closed connections,
// and notifies EdgeX if a Reader connection appears lost.
// It forwards ROAccessReports & ReaderEventNotifications to EdgeX.
//
// It is safe to use an LLRPDevice's methods concurrently and when disconnected.
// If TrySend fails due to connection issues,
// it reattempts the send a few times, backing off between attempts.
//
// Use Stop to stop actively managing the connection.
// After Stop, we no longer receive reports or event notifications,
// and all calls to TrySend will fail.
// At that point, to reconnect to the same Reader,
// you must create a new LLRPDevice instance.
type LLRPDevice struct {
	// Although the driver package can access these directly,
	// it's important to use the methods instead
	// to ensure it can properly handle connect/reconnect behavior.

	name string // comes from EdgeX; assumed not written after construction
	lc   logger.LoggingClient
	ch   chan<- *dsModels.AsyncValues

	deviceMu sync.RWMutex
	address  net.Addr
	// readerStart is used for the special case in which a Reader lacks a UTC clock.
	// It is not relevant if the Reader has a UTC clock.
	//
	// For ones that don't, the Reader sends timestamps as Uptime rather than UTC.
	// Effectively, this means is counting microseconds since it started
	// instead of microseconds since Jan 1, 1970,
	// so when we get the initial connect ReaderEventNotification,
	// we use our own clock to calculate when that Reader moment was in UTC.
	// We can then add this value to other Uptime parameters to convert them to UTC.
	//
	// This calculation does not account for leap seconds,
	// but neither does Go's stdlib Time package.
	readerStart time.Time
	isUp        bool // used for managing EdgeX opstate; isn't updated immediately

	clientLock sync.RWMutex // we'll recreate client if it closes; note its lock is independent
	client     *llrp.Client
	cancel     context.CancelFunc // stops the reconnect process
}

// NewLLRPDevice returns an LLRPDevice which attempts to connect to the given address.
func (d *Driver) NewLLRPDevice(name string, address net.Addr, opState contract.OperatingState) *LLRPDevice {
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
		ch:      d.asyncCh,
		isUp:    opState == contract.Up,
	}

	// These options will be used each time we reconnect.
	opts := []llrp.ClientOpt{
		llrp.WithLogger(&edgexLLRPClientLogger{devName: name, lc: d.lc}),
		llrp.WithMessageHandler(llrp.MsgROAccessReport, l.newROHandler()),
		llrp.WithMessageHandler(llrp.MsgReaderEventNotification, l.newReaderEventHandler(d.svc)),
		llrp.WithTimeout(keepAliveInterval * maxMissedKAs),
	}

	// Create the initial client, which we can immediately make Send requests to,
	// though they can't be processed until it successfully connects.
	l.client = llrp.NewClient(opts...)
	c := l.client
	dialer := net.Dialer{}

	// This is all captured in a context to avoid exterior race conditions.
	go func() {
		defer func() {
			cancel()
			rmvCtx, rmvCncl := context.WithTimeout(context.Background(), shutdownGrace)
			defer rmvCncl()
			d.removeDevice(rmvCtx, name)
		}()

		d.lc.Debug("Starting Reader management.", "device", name)

		// Until the context is canceled, attempt to dial and connect.
		for ctx.Err() == nil {
			// If the Client closes in a "normal" way while the context is still alive,
			// reset the retry/backoff policy and restart the dial/connect loop.
			_ = retry.Slow.RetryWithCtx(ctx, retry.Forever, func(ctx context.Context) (bool, error) {
				// If the Client connection closes with a failure,
				// backoff but try multiple times before considering it disconnected.
				err := retry.Quick.RetryWithCtx(ctx, maxConnAttempts, func(ctx context.Context) (bool, error) {
					// First, we establish a successful tcp connection.
					l.deviceMu.RLock()
					addr := l.address
					l.deviceMu.RUnlock()

					d.lc.Debug("Attempting to dial Reader.", "address", addr.String(), "device", name)
					dialCtx, dialCtxCancel := context.WithTimeout(ctx, dialTimeout)
					defer dialCtxCancel()
					conn, err := dialer.DialContext(dialCtx, addr.Network(), addr.String())
					if err != nil {
						d.lc.Error("Failed to dial Reader.", "error", err.Error(),
							"address", addr.String(), "device", name)
						return true, err
					}

					defer conn.Close()

					d.lc.Debug("Attempting LLRP Client connection.", "device", name)

					// Create a new LLRP Client on the connection.
					// This blocks until the Client closes.
					clientErr := c.Connect(conn)
					if errors.Is(clientErr, llrp.ErrClientClosed) {
						d.lc.Debug("LLRP Client connection closed normally.", "device", name)
						clientErr = nil // This resets the backoff/retry policy.
					} else if clientErr != nil {
						// Connect promises to always return some non-nil err,
						// but nothing about the language enforces that,
						// so here's a nil check on the off-chance that changes.
						// If that "contract" ever changes so that Connect returns nil,
						// it's more sensible to ignore it.
						// The only consequence is not printing the Debug message above.
						d.lc.Error("Client disconnected unexpectedly.",
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

				switch err {
				case nil:
					return true, nil // connection reset normally
				case context.Canceled:
					return false, err // device stopped normally
				}

				// Multiple attempts to connect have failed.
				// Tell EdgeX the device is disabled (if we haven't already).
				l.deviceMu.Lock()
				isEnabled := l.isUp
				l.isUp = false
				l.deviceMu.Unlock()

				if isEnabled {
					d.lc.Warn("Failed to connect to Device after multiple tries.", "device", name)
					if err := d.svc.UpdateDeviceOperatingState(name, contract.Down); err != nil {
						d.lc.Error("Failed to set device operating state to Disabled.",
							"device", name, "error", err.Error())
						// This is not likely, but might as well try again next round.
						l.deviceMu.Lock()
						l.isUp = true
						l.deviceMu.Unlock()
					}
				}

				return true, err // backoff & retry
			})
		}
	}()

	return l
}

// TrySend works like the llrp.Client's SendFor method,
// but reattempts a send a few times if it fails due to a closed reader.
// Additionally, it enforces our KeepAlive interval for timeout detection
// upon SetReaderConfig messages.
func (l *LLRPDevice) TrySend(ctx context.Context, request llrp.Outgoing, reply llrp.Incoming) error {
	if req, ok := request.(*llrp.SetReaderConfig); ok {
		ka := llrp.Millisecs32(keepAliveInterval.Milliseconds())
		if req.KeepAliveSpec != nil {
			reqKA := req.KeepAliveSpec
			if reqKA.Interval != ka || reqKA.Trigger != llrp.KATriggerPeriodic {
				l.lc.Warn("This device service enforces a KeepAlive interval "+
					"based on its connection timeout; "+
					"overriding the KeepAliveSpec in SetReaderConfig.",
					"requestedKA", req.KeepAliveSpec.Interval, "enforcedKA", ka)
				req.KeepAliveSpec.Interval = ka
				req.KeepAliveSpec.Trigger = llrp.KATriggerPeriodic
			}
		} else {
			l.lc.Info("Adding device-service-enforced a KeepAlive spec to ReaderConfig.",
				"forcedKA", keepAliveInterval)
			req.KeepAliveSpec = &llrp.KeepAliveSpec{
				Trigger:  llrp.KATriggerPeriodic,
				Interval: ka,
			}
		}
	}

	return retry.Quick.RetryWithCtx(ctx, maxSendAttempts, func(ctx context.Context) (bool, error) {
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
	l.deviceMu.Lock()
	old := l.address
	l.address = addr
	l.deviceMu.Unlock()

	if sameAddr(old, addr) {
		return nil
	}

	l.clientLock.Lock()
	defer l.clientLock.Unlock()
	return l.closeLocked(ctx)
}

// sameAddr returns true if both addresses are nil
// or if the address' String and Network compare equal.
func sameAddr(a1, a2 net.Addr) bool {
	if a1 == nil || a2 == nil {
		return true
	}
	return a1.String() == a2.String() && a1.Network() == a2.Network()
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
			err = fmt.Errorf("failed to shutdown gracefully: %w", err)
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

// resetConn closes the current client connection.
// The connection may attempt reconnection automatically.
func (l *LLRPDevice) resetConn() {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	l.clientLock.Lock()
	defer l.clientLock.Unlock()
	if err := l.closeLocked(ctx); err != nil && !errors.Is(err, llrp.ErrClientClosed) {
		// Short of a panic, logging is the only thing to be done here.
		l.lc.Error("Failed to close Reader connection.",
			"error", err.Error(), "device", l.name)
	}
}

// newReaderEventHandler returns an llrp.MessageHandler for ReaderEventNotifications.
//
// If the event is a new successful connection event,
// it ensures the Reader has our desired configuration state.
func (l *LLRPDevice) newReaderEventHandler(svc interfaces.DeviceServiceSDK) llrp.MessageHandler {
	return llrp.MessageHandlerFunc(func(c *llrp.Client, msg llrp.Message) {
		now := time.Now()

		event := &llrp.ReaderEventNotification{}
		if err := msg.UnmarshalTo(event); err != nil {
			l.lc.Error("Failed to unmarshal LLRP reader event notification", "error", err.Error())
			return
		}

		l.deviceMu.RLock()
		readerStart := l.readerStart
		l.deviceMu.RUnlock()

		renData := event.ReaderEventNotificationData
		if renData.UTCTimestamp == 0 && readerStart.IsZero() {
			readerStart = now.Add(-1 * time.Microsecond * time.Duration(renData.Uptime))
		}

		if !readerStart.IsZero() {
			renData.UTCTimestamp = uptimeToUTC(readerStart, renData.Uptime)
		}

		if renData.ConnectionAttemptEvent != nil &&
			llrp.ConnectionAttemptEventType(*renData.ConnectionAttemptEvent) == llrp.ConnSuccess {
			go func() {
				// Don't send the event until after processing a possible OpState change.
				l.onConnect(svc)
				l.sendEdgeXEvent(ResourceReaderNotification, now.UnixNano(), event)
			}()
		} else {
			go l.sendEdgeXEvent(ResourceReaderNotification, now.UnixNano(), event)
		}
	})
}

// sendEdgeXEvent creates an EdgeX Event for the LLRP event and sends it.
func (l *LLRPDevice) sendEdgeXEvent(eventName string, ns int64, event interface{}) {
	l.lc.Debugf("Sending LLRP Event '%s'", eventName)
	cmdValue, err := dsModels.NewCommandValueWithOrigin(eventName, common.ValueTypeObject, event, ns)
	if err != nil {
		l.lc.Errorf("Failed to create new command value with origin: %s", err.Error())
		return
	}

	l.ch <- &dsModels.AsyncValues{
		DeviceName:    l.name,
		CommandValues: []*dsModels.CommandValue{cmdValue},
	}
}

// newROHandler returns an llrp.MessageHandler to handle ROAccessReports.
func (l *LLRPDevice) newROHandler() llrp.MessageHandler {
	return llrp.MessageHandlerFunc(func(c *llrp.Client, msg llrp.Message) {
		now := time.Now()
		report := &llrp.ROAccessReport{}

		if err := msg.UnmarshalTo(report); err != nil {
			l.lc.Error("Failed to unmarshal async event from LLRP.", "error", err.Error())
			return
		}

		l.deviceMu.RLock()
		readerStart := l.readerStart
		l.deviceMu.RUnlock()

		go func() {
			if !readerStart.IsZero() {
				processReport(readerStart, report)
			}
			l.sendEdgeXEvent(ResourceROAccessReport, now.UnixNano(), report)
		}()
	})
}

func uptimeToUTC(readerStart time.Time, uptime llrp.Uptime) llrp.UTCTimestamp {
	// UTC of event = readerStartUTC + duration between reader start and event.
	// We have to divide by 1000 to get from nanosecs back to microsecs.
	return llrp.UTCTimestamp(readerStart.
		Add(time.Microsecond*time.Duration(uptime)).
		UnixNano() / 1000)
}

// processReport processes an llrp.ROAccessReport
// by setting UTC parameters from their Uptime values.
func processReport(readerStart time.Time, report *llrp.ROAccessReport) {
	// If the Reader has a UTC clock, we don't need to inspect the survey data.
	if readerStart.IsZero() {
		return
	}

	for i := range report.RFSurveyReportData {
		surveyData := &report.RFSurveyReportData[i]
		for j := range surveyData.FrequencyRSSILevelEntries {
			surveyData.FrequencyRSSILevelEntries[j].UTCTimestamp =
				uptimeToUTC(readerStart, surveyData.FrequencyRSSILevelEntries[j].Uptime)
		}
	}

	for i := range report.TagReportData {
		data := &report.TagReportData[i] // avoid copying the struct

		if data.FirstSeenUptime != nil {
			*data.FirstSeenUTC = llrp.FirstSeenUTC(uptimeToUTC(readerStart, llrp.Uptime(*data.FirstSeenUptime)))
		}

		if data.LastSeenUptime != nil {
			*data.LastSeenUTC = llrp.LastSeenUTC(uptimeToUTC(readerStart, llrp.Uptime(*data.LastSeenUptime)))
		}
	}
}

// onConnect is called when we open a new connection to a Reader.
func (l *LLRPDevice) onConnect(svc interfaces.DeviceServiceSDK) {
	l.deviceMu.RLock()
	isEnabled := l.isUp
	l.deviceMu.RUnlock()

	if !isEnabled {
		l.lc.Info("Device connection restored.", "device", l.name)
		if err := svc.UpdateDeviceOperatingState(l.name, contract.Up); err != nil {
			l.lc.Error("Failed to set device operating state to Enabled.",
				"device", l.name, "error", err.Error())
		} else {
			l.deviceMu.Lock()
			l.isUp = true
			l.deviceMu.Unlock()
		}
	}

	l.lc.Debug("Setting Reader KeepAlive spec.", "device", l.name)
	conf := &llrp.SetReaderConfig{
		KeepAliveSpec: &llrp.KeepAliveSpec{
			Trigger:  llrp.KATriggerPeriodic,
			Interval: llrp.Millisecs32(keepAliveInterval.Milliseconds()),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	if err := l.TrySend(ctx, conf, &llrp.SetReaderConfigResponse{}); err != nil {
		l.lc.Error("Failed to set KeepAlive interval.", "device", l.name, "error", err.Error())
		l.resetConn()
	}
}
