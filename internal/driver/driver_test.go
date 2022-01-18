//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/edgexfoundry/device-rfid-llrp-go/internal/llrp"
	dsModels "github.com/edgexfoundry/device-sdk-go/v2/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/clients/logger"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/common"
	"github.com/stretchr/testify/require"
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

	elog := getLogger()

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
		attribs map[string]interface{}
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
				Type:               common.ValueTypeObject,
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

			if common.ValueTypeObject != cv.Type {
				t.Errorf("expected %+v; got %+v", common.ValueTypeObject, cv.Type)
			}

			s, err := json.Marshal(cv)
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
	require.NoError(t, err)

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

	elog := getLogger()
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

	roSpecID, err := dsModels.NewCommandValueWithOrigin(ResourceROSpecID, common.ValueTypeUint32, uint32(1), 0)
	require.NoError(t, err)

	accessSpecID, err := dsModels.NewCommandValueWithOrigin(ResourceAccessSpecID, common.ValueTypeUint32, uint32(2), 0)
	require.NoError(t, err)

	customData := base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 0})
	t.Logf("%s", customData)

	readerConfigCmdValue, err := dsModels.NewCommandValueWithOrigin(ResourceReaderConfig, common.ValueTypeObject, make(map[string]interface{}), 0)
	require.NoError(t, err)

	roSpecCmdValue, err := dsModels.NewCommandValueWithOrigin(ResourceAction, common.ValueTypeString, ActionStart, 0)
	require.NoError(t, err)

	accessSpecCmdValue, err := dsModels.NewCommandValueWithOrigin(ResourceAction, common.ValueTypeString, ActionEnable, 0)
	require.NoError(t, err)

	customMessageCmdValue, err := dsModels.NewCommandValueWithOrigin("MyCustomMessage", common.ValueTypeString, customData, 0)
	require.NoError(t, err)

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
				Type:               common.ValueTypeObject,
			}},
			param: []*dsModels.CommandValue{
				readerConfigCmdValue,
			},
		},
		{
			name: "StartROSpec1",
			reqs: []dsModels.CommandRequest{{
				DeviceResourceName: ResourceROSpecID,
				Type:               common.ValueTypeString,
			}, {
				DeviceResourceName: ResourceAction,
				Type:               common.ValueTypeString,
			}},
			param: []*dsModels.CommandValue{
				roSpecID,
				roSpecCmdValue,
			},
		},
		{
			name: "EnableAccessSpec2",
			reqs: []dsModels.CommandRequest{{
				DeviceResourceName: ResourceAccessSpecID,
				Type:               common.ValueTypeString,
			}, {
				DeviceResourceName: ResourceAction,
				Type:               common.ValueTypeString,
			}},
			param: []*dsModels.CommandValue{
				accessSpecID,
				accessSpecCmdValue,
			}},
		{
			name: "CustomMessage",
			reqs: []dsModels.CommandRequest{{
				DeviceResourceName: "MyCustomMessage",
				Type:               common.ValueTypeString,
				Attributes: map[string]interface{}{
					"vendor":  "25882",
					"subtype": "21",
				}},
			},
			param: []*dsModels.CommandValue{
				customMessageCmdValue,
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			err := d.HandleWriteCommands(
				"localReader", protocolMap{},
				testCase.reqs, testCase.param,
			)

			require.NoError(t, err)
		})
	}
}

func getLogger() logger.LoggingClient {
	if testing.Verbose() {
		return logger.NewClient("unitTest", "DEBUG")
	} else {
		return logger.MockLogger{}
	}
}
