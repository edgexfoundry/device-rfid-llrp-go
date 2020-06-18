package driver

import (
	"encoding/binary"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type inetTest struct {
	inet  string
	first string
	last  string
	size  uint32
	err   bool
}

func TestMain(m *testing.M) {
	NewProtocolDriver()
	driver.lc = logger.NewClient("test", false, "", "DEBUG")

	os.Exit(m.Run())
}

func TestAutoDiscover(t *testing.T) {
	autoDiscover()
}

// computeNetSz computes the amount of IPs in a subnet for testing purposes
func computeNetSz(subnetSz int) uint32 {
	if subnetSz >= 31 {
		return 1
	}
	return ^uint32(0)>>subnetSz - 1
}

func testIpGenerator(input inetTest) (result inetTest) {
	var wg sync.WaitGroup
	ipCh := make(chan uint32)
	netCh := make(chan *net.IPNet)
	go ipGenerator(&wg, netCh, ipCh)

	wg.Add(1)
	go func() {
		_, inet, err := net.ParseCIDR(input.inet)
		if err != nil {
			result.err = true
		} else {
			netCh <- inet
		}
		close(netCh)
		wg.Done()
	}()

	go func() {
		ip := net.IP([]byte{0, 0, 0, 0})

		for a := range ipCh {
			atomic.AddUint32(&result.size, 1)
			binary.BigEndian.PutUint32(ip, a)
			result.last = ip.String()
			if result.first == "" {
				result.first = ip.String()
			}
			wg.Done()
		}
	}()

	time.Sleep(1 * time.Second)
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
		//{
		//	inet:  "192.168.1.110/32",
		//	first: "192.168.1.110",
		//	last:  "192.168.1.110",
		//	size:  computeNetSz(32),
		//},
		//{
		//	inet:  "192.168.1.20/31",
		//	first: "192.168.1.20",
		//	last:  "192.168.1.20",
		//	size:  computeNetSz(31),
		//},
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
		t.Run(input.inet, func(t *testing.T) {
			result := testIpGenerator(input)
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

func TestVirtualRegex_virtual(t *testing.T) {
	ifaces := []string{"docker0", "docker_gwbridge", "br-123456", "veth12", "virbr0-nic"}

	for _, iface := range ifaces {
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
		t.Run(iface, func(t *testing.T) {
			if virtualRegex.MatchString(iface) {
				t.Errorf("expected interface %s to be detected as real, but was detected as virtual", iface)
			}
		})
	}
}
