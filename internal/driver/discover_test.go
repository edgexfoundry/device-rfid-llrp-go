//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"encoding/binary"
	"fmt"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

type inetTest struct {
	inet  string
	first string
	last  string
	size  uint32
	err   bool
}

var (
	svc = NewMockSdkService()
)

func TestMain(m *testing.M) {
	NewProtocolDriver()

	driver.svc = svc
	driver.lc = logger.NewClient("test", false, "", "DEBUG")
	driver.config = &driverConfiguration{
		DiscoverySubnets:    []string{"127.0.0.1/32"},
		ProbeAsyncLimit:     1000,
		ProbeTimeoutSeconds: 1,
		ScanPort:            "50923",
	}

	os.Exit(m.Run())
}

func TestAutoDiscover(t *testing.T) {
	t.Parallel()
	// attempt to discover without emulator, expect none found
	svc.clearDevices()
	discovered := autoDiscover()
	if len(discovered) != 0 {
		t.Fatalf("expected 0 discovered devices, however got: %d", len(discovered))
	}

	// attempt to discover WITH emulator, expect emulator to be found
	port, err := strconv.Atoi(driver.config.ScanPort)
	if err != nil {
		t.Fatalf("Failed to parse driver.config.ScanPort, unable to run discovery tests. value = %v" + driver.config.ScanPort)
	}
	emu := llrp.NewTestEmulator()
	if err := emu.StartAsync(port); err != nil {
		t.Fatal("unable to start emulator: " + err.Error())
	}

	readerConfig := llrp.GetReaderConfigResponse{
		Identification: &llrp.Identification{
			IDType:   llrp.ID_MAC_EUI64,
			ReaderID: []byte{0x00, 0x00, 0x00, 0x00, 0x19, 0xC5, 0xD6},
		},
	}
	emu.SetResponse(llrp.MsgGetReaderConfig, &readerConfig)

	readerCaps := llrp.GetReaderCapabilitiesResponse{
		GeneralDeviceCapabilities: &llrp.GeneralDeviceCapabilities{
			MaxSupportedAntennas:    4,
			CanSetAntennaProperties: false,
			HasUTCClock:             true,
			DeviceManufacturer:      uint32(Impinj),
			Model:                   uint32(SpeedwayR420),
			FirmwareVersion:         "5.14.0.240",
		},
	}
	emu.SetResponse(llrp.MsgGetReaderCapabilities, &readerCaps)

	discovered = autoDiscover()
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered device, however got: %d", len(discovered))
	}
	if discovered[0].Name != "SpeedwayR-19-C5-D6" {
		t.Errorf("expected discovered device's name to be SpeedwayR-19-C5-D6, but was: %s", discovered[0].Name)
	}
	svc.AddDiscoveredDevices(discovered)

	// attempt to discover again WITH emulator, however expect emulator to be skipped
	svc.resetAddedCount()
	discovered = autoDiscover()
	if len(discovered) != 0 {
		t.Fatalf("expected no devices to be discovered, but was %d", len(discovered))
	}

	// update reader id and model information
	readerConfig.Identification.ReaderID = []byte{0x00, 0x00, 0x00, 0x00, 0x25, 0x9C, 0xD4}
	readerCaps.GeneralDeviceCapabilities.Model = uint32(XArray)
	// clear and re-discover
	svc.clearDevices()
	discovered = autoDiscover()
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered device, however got: %d", len(discovered))
	}
	if discovered[0].Name != "xArray-25-9C-D4" {
		t.Errorf("expected discovered device's name to be xArray-25-9C-D4, but was: %s", discovered[0].Name)
	}

	// update reader id and model information
	readerConfig.Identification.ReaderID = []byte{0x00, 0x00, 0x00, 0x00, 0xFC, 0x4D, 0x1A}
	readerCaps.GeneralDeviceCapabilities.DeviceManufacturer = uint32(0x32) // unknown vendor
	readerCaps.GeneralDeviceCapabilities.Model = uint32(0x32)              // unknown model
	// clear and re-discover
	svc.clearDevices()
	discovered = autoDiscover()
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered device, however got: %d", len(discovered))
	}
	if discovered[0].Name != "LLRP-FC-4D-1A" {
		t.Errorf("expected discovered device's name to be LLRP-FC-4D-1A, but was: %s", discovered[0].Name)
	}

	// update reader id and model information
	readerConfig.Identification.IDType = llrp.ID_EPC                                        // test non-mac id types
	readerConfig.Identification.ReaderID = []byte{0x00, 0x1A, 0x00, 0x4F, 0xD9, 0xCA, 0x2B} // will be used as-is, not parsed
	// clear and re-discover
	svc.clearDevices()
	discovered = autoDiscover()
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered device, however got: %d", len(discovered))
	}
	if discovered[0].Name != "LLRP-001a004fd9ca2b" {
		t.Errorf("expected discovered device's name to be LLRP-001a004fd9ca2b, but was: %s", discovered[0].Name)
	}

	if err := emu.Shutdown(); err != nil {
		t.Errorf("error shutting down test emulator: %s", err.Error())
	}
	// reset
	svc.clearDevices()
}

// computeNetSz computes the amount of IPs in a subnet for testing purposes
func computeNetSz(subnetSz int) uint32 {
	if subnetSz >= 31 {
		return 1
	}
	return ^uint32(0)>>subnetSz - 1
}

func mockIpWorker(ipCh <-chan uint32, result *inetTest) {
	ip := net.IP([]byte{0, 0, 0, 0})
	var last uint32

	for a := range ipCh {
		atomic.AddUint32(&result.size, 1)
		atomic.StoreUint32(&last, a)

		if result.first == "" {
			binary.BigEndian.PutUint32(ip, a)
			result.first = ip.String()
		}
		binary.BigEndian.PutUint32(ip, last)
		result.last = ip.String()
	}

}

func ipGeneratorTest(input inetTest) (result inetTest) {
	var wg sync.WaitGroup
	ipCh := make(chan uint32, input.size)

	wg.Add(1)
	go func() {
		defer wg.Done()
		mockIpWorker(ipCh, &result)
	}()

	_, inet, err := net.ParseCIDR(input.inet)
	if err != nil {
		result.err = true
		return result
	}
	ipGenerator(inet, ipCh)
	close(ipCh)
	wg.Wait()

	return result
}

func TestIpGenerator(t *testing.T) {
	tests := []inetTest{
		{
			inet:  "192.168.1.110/24",
			first: "192.168.1.1",
			last:  "192.168.1.254",
			size:  computeNetSz(24),
		},
		{
			inet:  "192.168.1.110/32",
			first: "192.168.1.110",
			last:  "192.168.1.110",
			size:  computeNetSz(32),
		},
		{
			inet:  "192.168.1.20/31",
			first: "192.168.1.20",
			last:  "192.168.1.20",
			size:  computeNetSz(31),
		},
		{
			inet:  "192.168.99.20/16",
			first: "192.168.0.1",
			last:  "192.168.255.254",
			size:  computeNetSz(16),
		},
		{
			inet:  "10.10.10.10/8",
			first: "10.0.0.1",
			last:  "10.255.255.254",
			size:  computeNetSz(8),
		},
	}
	for _, input := range tests {
		input := input
		t.Run(input.inet, func(t *testing.T) {
			result := ipGeneratorTest(input)
			if result.err && !input.err {
				t.Error("got unexpected error")
			} else if !result.err && input.err {
				t.Error("expected an error, but no error was returned")
			} else {
				if result.size != input.size {
					t.Errorf("expected %d ips, but got %d", input.size, result.size)
				}
				if result.first != input.first {
					t.Errorf("expected first ip in range to be %s, but got %s", input.first, result.first)
				}
				if result.last != input.last {
					t.Errorf("expected last ip in range to be %s, but got %s", input.last, result.last)
				}
			}
		})
	}
}

func TestIpGeneratorSubnetSizes(t *testing.T) {
	for i := 32; i >= 10; i-- {
		i := i
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			result := ipGeneratorTest(inetTest{size: uint32(i), inet: fmt.Sprintf("192.168.1.1/%d", i)})
			if result.size != computeNetSz(i) {
				t.Errorf("expected %d ips, but got %d", computeNetSz(i), result.size)
			}
		})
	}
}
