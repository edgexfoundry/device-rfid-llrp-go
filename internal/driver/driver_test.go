//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"encoding/json"
	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	"github.com/pkg/errors"
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

func (e edgexCompatTestLogger) SetLogLevel(logLevel string) error {
	return nil
}

func (e edgexCompatTestLogger) Debug(msg string, args ...interface{}) {
	e.Logf("DEBUG: "+msg, args...)
}

func (e edgexCompatTestLogger) Error(msg string, args ...interface{}) {
	e.Logf("ERROR: "+msg, args...)
}

func (e edgexCompatTestLogger) Info(msg string, args ...interface{}) {
	e.Logf(" INFO: "+msg, args...)
}

func (e edgexCompatTestLogger) Trace(msg string, args ...interface{}) {
	e.Logf("TRACE: "+msg, args...)
}

func (e edgexCompatTestLogger) Warn(msg string, args ...interface{}) {
	e.Logf(" WARN: "+msg, args...)
}

func TestHandleRead(t *testing.T) {
	rfid, err := llrp.NewTestDevice(llrp.Version1_0_1, llrp.Version1_1, time.Second*1)
	if err != nil {
		t.Fatal(err)
	}

	c := rfid.Client
	connErrs := make(chan error)
	go func() {
		defer close(connErrs)
		connErrs <- c.Connect()
	}()

	t.Cleanup(func() {
		if err := rfid.Close(); err != nil {
			t.Errorf("%+v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
		defer cancel()
		if err := c.Shutdown(ctx); err != nil {
			if err := c.Close(); err != nil {
				t.Errorf("%+v", err)
			}
			t.Errorf("%+v", err)
		}

		for err := range connErrs {
			if !errors.Is(err, llrp.ErrClientClosed) {
				t.Errorf("%+v", err)
			}
		}
	})

	rfid.SetResponse(llrp.MsgGetReaderCapabilities, &llrp.GetReaderCapabilitiesResponse{
		GeneralDeviceCapabilities: &llrp.GeneralDeviceCapabilities{
			MaxSupportedAntennas: 1,
			HasUTCClock:          true,
			DeviceManufacturer:   12345,
			Model:                67890,
			FirmwareVersion:      "0.1.0",
			ReceiveSensitivities: []llrp.ReceiveSensitivityTableEntry{
				{Index: 1, ReceiveSensitivity: 0},
				{Index: 2, ReceiveSensitivity: 10},
				{Index: 3, ReceiveSensitivity: 100},
			},
		},
		LLRPCapabilities: &llrp.LLRPCapabilities{
			MaxPriorityLevelSupported:           1,
			MaxROSpecs:                          1,
			MaxSpecsPerROSpec:                   1,
			MaxInventoryParameterSpecsPerAISpec: 1,
			MaxAccessSpecs:                      1,
			MaxOpSpecsPerAccessSpec:             1,
		},
		RegulatoryCapabilities: &llrp.RegulatoryCapabilities{
			CountryCode:            840,
			CommunicationsStandard: 1,
			UHFBandCapabilities: &llrp.UHFBandCapabilities{
				TransmitPowerLevels: []llrp.TransmitPowerLevelTableEntry{
					{Index: 1, TransmitPowerValue: 1000},
					{Index: 2, TransmitPowerValue: 3000},
				},
				FrequencyInformation: llrp.FrequencyInformation{
					Hopping: true,
					FrequencyHopTables: []llrp.FrequencyHopTable{
						{
							HopTableID: 1,
							Frequencies: []llrp.KiloHertz{
								909250,
								908250,
								925750,
								911250,
								910750,
							},
						},
					},
				},
				C1G2RFModes: llrp.UHFC1G2RFModeTable{
					UHFC1G2RFModeTableEntries: []llrp.UHFC1G2RFModeTableEntry{
						{
							ModeID:                0,
							DivideRatio:           1,
							ForwardLinkModulation: 2,
							SpectralMask:          2,
							BackscatterDataRate:   640000,
							PIERatio:              1500,
							MinTariTime:           6250,
							MaxTariTime:           6250,
						},
					},
				},
				RFSurveyFrequencyCapabilities: nil,
			},
			Custom: nil,
		},
		C1G2LLRPCapabilities: &llrp.C1G2LLRPCapabilities{},
	})
	go rfid.ImpersonateReader()

	d := &Driver{
		lc:      edgexCompatTestLogger{t},
		clients: map[string]*llrp.Client{"localReader": c},
	}

	// Now we can start the test.
	cvs, err := d.HandleReadCommands("localReader", protocolMap{}, []dsModels.CommandRequest{{
		DeviceResourceName: ResourceReaderCap,
		Attributes:         nil,
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

	if ResourceReaderCap != cv.DeviceResourceName {
		t.Errorf("expected %s; got %s", ResourceReaderCap, cv.DeviceResourceName)
	}

	if dsModels.String != cv.Type {
		t.Errorf("expected %v; got %v", dsModels.String, cv.Type)
	}

	s, err := cv.StringValue()
	if err != nil {
		t.Errorf("%+v", err)
	}

	resp := llrp.GetReaderCapabilitiesResponse{}
	if err := json.Unmarshal([]byte(s), &resp); err != nil {
		t.Errorf("%+v", err)
	}
}
