package driver

import (
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	"math"
	"net"
	"reflect"
	"testing"
)

func init() {
	NewProtocolDriver()
	driver.lc = logger.NewClient("test", false, "", "DEBUG")
}

func TestDiscover(t *testing.T) {
	_, _ = Discover()
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

func TestIncrementIPv4(t *testing.T) {
	tests := []struct {
		ip   string
		next string
	}{
		{ip: "192.168.1.1", next: "192.168.1.2"},
		{ip: "255.255.255.255", next: "0.0.0.0"},
		{ip: "0.0.0.0", next: "0.0.0.1"},
		{ip: "192.0.255.255", next: "192.1.0.0"},
		{ip: "10.10.10.255", next: "10.10.11.0"},
	}
	for _, test := range tests {
		t.Run(test.ip, func(t *testing.T) {
			ip := net.ParseIP(test.ip).To4()
			err := incrementIPv4(ip)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(ip, net.ParseIP(test.next).To4()) {
				t.Errorf("expected %v but got %v", test.next, ip.String())
			}
		})
	}
}

func TestIncrementIPv4_Negative(t *testing.T) {
	ip := net.ParseIP("1.1.1.1.1.1").To16()
	if err := incrementIPv4(ip); err != ErrInvalidIPv4 {
		t.Errorf("expected ErrInvalidIPv4 but got: %v", err)
	}
}

func computeNetSz(subnetSz int) int {
	return int(math.Max(1, math.Round(math.Pow(2, float64(32-subnetSz))-2)))
}

func TestExpandIPNet(t *testing.T) {
	tests := []struct {
		inet  string
		first string
		last  string
		size  int
		err   bool
	}{
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
	for _, test := range tests {
		t.Run(test.inet, func(t *testing.T) {
			_, ipnet, _ := net.ParseCIDR(test.inet)
			ips, err := expandIPNet(ipnet)
			if err != nil && test.err == false {
				t.Errorf("got unexpected error: %v", err)
			} else if err == nil && test.err == true {
				t.Error("expected an error, but no error was returned")
			} else {
				if len(ips) != test.size {
					t.Errorf("expected %d ips, but got %d", test.size, len(ips))
				}
				if !reflect.DeepEqual(ips[0], net.ParseIP(test.first).To4()) {
					t.Errorf("expected first ip in range to be %s, but got %s", test.first, ips[0].String())
				}
				if !reflect.DeepEqual(ips[len(ips)-1], net.ParseIP(test.last).To4()) {
					t.Errorf("expected last ip in range to be %s, but got %s", test.last, ips[len(ips)-1].String())
				}
			}
		})
	}
}
