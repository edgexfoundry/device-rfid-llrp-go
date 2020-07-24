//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"encoding/json"
	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
	"testing"
	"time"
)

func TestGetTCPAddr(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		for _, m := range []protocolMap{{
			"tcp": {"host": "127.0.0.1", "port": "1234"},
		}} {
			addr, err := getAddr(m)
			if err != nil {
				t.Error(err)
			}

			expected := "127.0.0.1:1234"
			if expected != addr.String() {
				t.Errorf("expected %s; got %s", expected, addr.String())
			}
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		for _, m := range []protocolMap{{
			"tcp": {"host": "127.0.0.1", "port": "86492"},
		}} {
			if _, err := getAddr(m); err == nil {
				t.Error("expected an error, but didn't get one")
			}
		}
	})
}

type edgexCompatTestLogger struct {
	*testing.T
}

func (e edgexCompatTestLogger) SetLogLevel(_ string) error {
	return nil
}

func (e edgexCompatTestLogger) Trace(msg string, args ...interface{}) {
	if testing.Verbose() {
		e.Logf("TRACE: "+msg, args...)
	}
}

func (e edgexCompatTestLogger) Debug(msg string, args ...interface{}) {
	if testing.Verbose() {
		e.Logf("DEBUG: "+msg, args...)
	}
}

func (e edgexCompatTestLogger) Info(msg string, args ...interface{}) {
	if testing.Verbose() {
		e.Logf(" INFO: "+msg, args...)
	}
}

func (e edgexCompatTestLogger) Warn(msg string, args ...interface{}) {
	if testing.Verbose() {
		e.Logf(" WARN: "+msg, args...)
	}
}

func (e edgexCompatTestLogger) Error(msg string, args ...interface{}) {
	e.Logf("ERROR: "+msg, args...)
}

func TestHandleRead(t *testing.T) {
	// c := llrp.GetFunctionalClient(t, "192.168.86.88:5084")

	rfid, err := llrp.NewTestDevice(llrp.Version1_0_1, llrp.Version1_1, time.Second*1, !testing.Verbose())
	if err != nil {
		t.Fatal(err)
	}

	rfid.SetResponse(llrp.MsgGetReaderCapabilities, &llrp.GetReaderCapabilitiesResponse{})
	rfid.SetResponse(llrp.MsgGetReaderConfig, &llrp.GetReaderConfigResponse{})
	rfid.SetResponse(llrp.MsgGetROSpecs, &llrp.GetROSpecsResponse{})
	rfid.SetResponse(llrp.MsgGetAccessSpecs, &llrp.GetAccessSpecsResponse{})

	go rfid.ImpersonateReader()
	c := rfid.ConnectClient(t)

	d := &Driver{
		lc:            edgexCompatTestLogger{t},
		activeDevices: make(map[string]*Lurpper),
	}

	// This is a bit dirty. The point of the Lurpper is largely for retry logic,
	// but that's a real pain without abstracting around something like a net.Listener.
	d.activeDevices["localReader"] = &Lurpper{
		name:   "localReader",
		client: c,
	}

	for _, testCase := range []struct {
		name   string
		target llrp.Incoming
	}{
		{name: ResourceReaderCap, target: &llrp.GetReaderCapabilitiesResponse{}},
		{name: ResourceReaderConfig, target: &llrp.GetReaderConfigResponse{}},
		{name: ResourceROSpec, target: &llrp.GetROSpecsResponse{}},
		{name: ResourceAccessSpec, target: &llrp.GetAccessSpecsResponse{}},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {

			spec := llrp.NewROSpec()
			spec.ROBoundarySpec.StartTrigger.Trigger = llrp.ROStartTriggerNone

			cvs, err := d.HandleReadCommands("localReader", protocolMap{}, []dsModels.CommandRequest{{
				DeviceResourceName: testCase.name,
				Type:               dsModels.String,
			}})
			if err != nil {
				t.Fatal(err)
			}

			if len(cvs) != 1 {
				t.Fatalf("expected exactly one command value; got %d", len(cvs))
			}

			cv := cvs[0]
			if cv == nil {
				t.Fatal("command value is nil")
			}

			if testCase.name != cv.DeviceResourceName {
				t.Errorf("expected %s; got %s", testCase.name, cv.DeviceResourceName)
			}

			if dsModels.String != cv.Type {
				t.Errorf("expected %v; got %v", dsModels.String, cv.Type)
			}

			s, err := cv.StringValue()
			if err != nil {
				t.Errorf("%+v", err)
			}

			if err := json.Unmarshal([]byte(s), &testCase.target); err != nil {
				t.Errorf("%+v", err)
			}
		})
	}
}
