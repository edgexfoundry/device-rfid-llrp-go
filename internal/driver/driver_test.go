//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"encoding/base64"
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

	elog := edgexCompatTestLogger{t}
	d := &Driver{
		lc:            elog,
		activeDevices: make(map[string]*LLRPDevice),
	}

	// This is a bit dirty. The point of the LLRPDevice is largely for retry logic,
	// but that's a real pain without abstracting around something like a net.Listener.
	d.activeDevices["localReader"] = &LLRPDevice{
		name:   "localReader",
		client: c,
		lc:     elog,
	}

	for _, testCase := range []struct {
		name    string
		target  llrp.Incoming
		attribs map[string]string
	}{
		{name: ResourceReaderCap, target: &llrp.GetReaderCapabilitiesResponse{}},
		{name: ResourceReaderConfig, target: &llrp.GetReaderConfigResponse{}},
		{name: ResourceROSpec, target: &llrp.GetROSpecsResponse{}},
		{name: ResourceAccessSpec, target: &llrp.GetAccessSpecsResponse{}},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			cvs, err := d.HandleReadCommands("localReader", protocolMap{}, []dsModels.CommandRequest{{
				DeviceResourceName: testCase.name,
				Type:               dsModels.String,
				Attributes:         testCase.attribs,
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

func TestHandleWrite(t *testing.T) {
	rfid, err := llrp.NewTestDevice(llrp.Version1_0_1, llrp.Version1_1, time.Second*1, !testing.Verbose())
	if err != nil {
		t.Fatal(err)
	}

	rfid.SetResponse(llrp.MsgSetReaderConfig, &llrp.SetReaderConfigResponse{})
	rfid.SetResponse(llrp.MsgStartROSpec, &llrp.StartROSpecResponse{})
	rfid.SetResponse(llrp.MsgEnableAccessSpec, &llrp.EnableAccessSpecResponse{})
	rfid.SetResponse(llrp.MsgCustomMessage, &llrp.CustomMessage{
		VendorID:       1234,
		MessageSubtype: 22,
		Data:           []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 0},
	})

	go rfid.ImpersonateReader()
	c := rfid.ConnectClient(t)

	elog := edgexCompatTestLogger{t}
	d := &Driver{
		lc:            elog,
		activeDevices: make(map[string]*LLRPDevice),
	}

	// This is a bit dirty. The point of the LLRPDevice is largely for retry logic,
	// but that's a real pain without abstracting around something like a net.Listener.
	d.activeDevices["localReader"] = &LLRPDevice{
		name:   "localReader",
		client: c,
		lc:     elog,
	}

	roSpecID, err := dsModels.NewUint32Value(ResourceROSpecID, 0, 1)
	if err != nil {
		t.Fatalf("failed to make ROSpecID: %+v", err)
	}

	accessSpecID, err := dsModels.NewUint32Value(ResourceAccessSpecID, 0, 2)
	if err != nil {
		t.Fatalf("failed to make AccessSpecID: %+v", err)
	}

	customData := base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 0})
	t.Logf("%s", customData)

	for _, testCase := range []struct {
		name    string
		reqs    []dsModels.CommandRequest
		param   []*dsModels.CommandValue
		attribs map[string]string
	}{
		{
			name: "SetReaderConfig",
			reqs: []dsModels.CommandRequest{{
				DeviceResourceName: ResourceReaderConfig,
				Type:               dsModels.String,
			}},
			param: []*dsModels.CommandValue{
				dsModels.NewStringValue(ResourceReaderConfig, 0, "{}"),
			},
		},
		{
			name: "StartROSpec1",
			reqs: []dsModels.CommandRequest{{
				DeviceResourceName: ResourceROSpecID,
				Type:               dsModels.String,
			}, {
				DeviceResourceName: ResourceAction,
				Type:               dsModels.String,
			}},
			param: []*dsModels.CommandValue{
				roSpecID,
				dsModels.NewStringValue(ResourceAction, 0, ActionStart),
			},
		},
		{
			name: "EnableAccessSpec2",
			reqs: []dsModels.CommandRequest{{
				DeviceResourceName: ResourceAccessSpecID,
				Type:               dsModels.String,
			}, {
				DeviceResourceName: ResourceAction,
				Type:               dsModels.String,
			}},
			param: []*dsModels.CommandValue{
				accessSpecID,
				dsModels.NewStringValue(ResourceAction, 0, ActionEnable),
			}},
		{
			name: "CustomMessage",
			reqs: []dsModels.CommandRequest{{
				DeviceResourceName: "MyCustomMessage",
				Type:               dsModels.String,
				Attributes: map[string]string{
					"vendor":  "25882",
					"subtype": "21",
				}},
			},
			param: []*dsModels.CommandValue{
				dsModels.NewStringValue("MyCustomMessage", 0, customData),
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			err := d.HandleWriteCommands(
				"localReader", protocolMap{},
				testCase.reqs, testCase.param,
			)

			if err != nil {
				t.Fatalf("%+v", err)
			}
		})
	}
}
