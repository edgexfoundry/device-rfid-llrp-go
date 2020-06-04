package driver

import (
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	"net"
	"os"
	"reflect"
	"testing"
)

func TestMain(m *testing.M) {
	NewProtocolDriver()
	driver.lc = logger.NewClient("test", false, "", "DEBUG")

	os.Exit(m.Run())
}

func TestScanner(t *testing.T) {
	mon, wg, err := RunScanner()
	if err != nil {
		t.Fatal(err)
	}
	mon.Stop()
	wg.Wait()
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

func TestIncrementIP(t *testing.T) {
	tests := []struct {
		ip   string
		next string
	}{
		{ip: "192.168.1.1", next: "192.168.1.2"},
		{ip: "255.255.255.255", next: "0.0.0.0"},
		{ip: "0.0.0.0", next: "1.0.0.0.0"},
		{ip: "192.0.255.255", next: "192.1.0.0"},
		{ip: "10.10.10.255", next: "10.10.11.0"},
	}
	for _, test := range tests {
		t.Run(test.ip, func(t *testing.T) {
			ip := net.ParseIP(test.ip)
			incrementIP(ip)
			if !reflect.DeepEqual(ip, net.ParseIP(test.next)) {
				t.Errorf("expected %v but got %v", test.next, ip.String())
			}
		})
	}
}
