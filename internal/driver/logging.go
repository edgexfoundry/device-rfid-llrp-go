//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"github.com/edgexfoundry/device-rfid-llrp-go/internal/llrp"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
)

// edgexLLRPClientLogger implements the llrp.ClientLogger interface
// by forwarding messages to EdgeX's LoggingClient with the device name attached.
type edgexLLRPClientLogger struct {
	devName string
	lc      logger.LoggingClient
}

func (l *edgexLLRPClientLogger) SendingMsg(h llrp.Header) {
	switch h.Type() {
	case llrp.MsgKeepAliveAck:
		l.lc.Debug("Sending LLRP message", "type", h.Type().String(), "device", l.devName)
	default:
		l.lc.Info("Sending LLRP message", "type", h.Type().String(), "device", l.devName)
	}
}

func (l *edgexLLRPClientLogger) ReceivedMsg(h llrp.Header, ver llrp.VersionNum) {
	switch h.Type() {
	case llrp.MsgKeepAlive, llrp.MsgROAccessReport:
		l.lc.Debug("Incoming LLRP message", "type", h.Type().String(), "device", l.devName)
	default:
		l.lc.Info("Incoming LLRP message", "type", h.Type().String(), "device", l.devName)
	}

	if ver != h.Version() {
		l.lc.Warn("LLRP incoming message version mismatch",
			"message-version", h.Version().String(),
			"client-version", ver.String())
	}
}

func (l *edgexLLRPClientLogger) MsgHandled(h llrp.Header) {
	l.lc.Debug("Handled LLRP message.", "type", h.Type().String(), "device", l.devName)
}

func (l *edgexLLRPClientLogger) MsgUnhandled(h llrp.Header) {
	l.lc.Debug("Ignored LLRP message.", "type", h.Type().String(), "device", l.devName)
}

func (l *edgexLLRPClientLogger) HandlerPanic(h llrp.Header, err error) {
	l.lc.Error("LLRP message handler panic'd (recovered).",
		"type", h.Type().String(), "device", l.devName, "error", err.Error())
}
