//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"encoding/binary"
	"fmt"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
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
	Instance()
	driver.svc = svc
	driver.lc = logger.NewClient("test", false, "", "DEBUG")

	os.Exit(m.Run())
}

func TestAutoDiscover(t *testing.T) {
	oldPortStr := llrpPortStr
	llrpPortStr = "50923"

	// attempt to discover without emulator, expect none found
	svc.clearDevices()
	discovered := autoDiscover()
	if len(discovered) != 0 {
		t.Fatalf("expected 0 discovered devices, however got: %d", len(discovered))
	}

	// attempt to discover WITH emulator, expect emulator to be found
	port, _ := strconv.Atoi(llrpPortStr)
	go emuServer(port)
	discovered = autoDiscover()
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered device, however got: %d", len(discovered))
	}
	svc.AddDiscoveredDevices(discovered)

	// attempt to discover again WITH emulator, however expect emulator to be skipped
	svc.resetAddedCount()
	go emuServer(port)
	discovered = autoDiscover()
	if len(discovered) != 0 {
		t.Fatalf("expected no devices to be discovered, but was %d", len(discovered))
	}

	// reset
	svc.clearDevices()
	llrpPortStr = oldPortStr
}

// computeNetSz computes the amount of IPs in a subnet for testing purposes
func computeNetSz(subnetSz int) uint32 {
	if subnetSz >= 31 {
		return 1
	}
	return ^uint32(0)>>subnetSz - 1
}

func mockIpWorker(wg *sync.WaitGroup, ipCh <-chan uint32, result *inetTest) {
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

		wg.Done()
	}

}

func mockIPv4NetGenerator(input inetTest, result *inetTest) <-chan *net.IPNet {
	netCh := make(chan *net.IPNet)
	go func() {
		_, inet, err := net.ParseCIDR(input.inet)
		if err != nil {
			result.err = true
		} else {
			netCh <- inet
		}
		close(netCh)
	}()
	return netCh
}

func ipGeneratorTest(input inetTest) (result inetTest) {
	var wg sync.WaitGroup
	ipCh := make(chan uint32, input.size)

	netCh := mockIPv4NetGenerator(input, &result)
	go mockIpWorker(&wg, ipCh, &result)

	ipGenerator(&wg, netCh, ipCh)
	wg.Wait()
	close(ipCh)

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

func TestVirtualRegex_virtual(t *testing.T) {
	ifaces := []string{"docker0", "docker_gwbridge", "br-123456", "veth12", "virbr0-nic"}

	for _, iface := range ifaces {
		iface := iface
		t.Run(iface, func(t *testing.T) {
			if !virtualRegex.MatchString(iface) {
				t.Errorf("expected interface %s to be detected as virtual, but was detected as real", iface)
			}
		})
	}
}

func TestVirtualRegex_notVirtual(t *testing.T) {
	ifaces := []string{"eth0", "eno1", "enp13s0"}

	for _, iface := range ifaces {
		iface := iface
		t.Run(iface, func(t *testing.T) {
			if virtualRegex.MatchString(iface) {
				t.Errorf("expected interface %s to be detected as real, but was detected as virtual", iface)
			}
		})
	}
}
