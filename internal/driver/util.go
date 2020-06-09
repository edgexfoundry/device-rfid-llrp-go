package driver

import (
	"fmt"
	sdk "github.com/edgexfoundry/device-sdk-go/pkg/service"
	edgexModels "github.com/edgexfoundry/go-mod-core-contracts/models"
	"net"
)

// incrementIP will increment an ip address to the next viable address
// IP addresses are stored as byte slices
// it starts at the last byte and increments it. because a byte is unsigned, it will wrap at 255 back to 0.
// when it overflows to 0 it will continue to the next higher level byte, otherwise it will stop incrementing
func incrementIP(ip net.IP) {
	// iterate over the bytes of the ip backwards
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++

		if ip[i] > 0 {
			// no overflow has occurred, we are done
			break
		}
	}
}

// getIPv4Nets will return all of the valid IPv4 IP networks the local machine is associated with.
// It checks all of the network interfaces that are up and not loopback interfaces.
// It only supports IPv4 networks
// `scanVirtualInterfaces` will determine whether or not to include virtual interfaces in the returned list
func getIPv4Nets(includeVirtual bool) ([]*net.IPNet, error) {
	var nets []*net.IPNet

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}

		if !includeVirtual && virtualRegex.MatchString(iface.Name) {
			driver.lc.Info("Skipping virtual network interface: " + iface.Name)
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
				// only allow ipv4
				if ip == nil || ip.To4() == nil {
					continue
				}

				driver.lc.Info(iface.Name)
				driver.lc.Info(fmt.Sprintf("%v", v))
				nets = append(nets, v)
			}
		}
	}

	return nets, nil
}

func makeDeviceName(ip string, port string) string {
	return ip + "_" + port
}

// registerDeviceIfNeeded takes the host and port number of a discovered LLRP reader and registers it for
// use with EdgeX
func registerDeviceIfNeeded(ip string, port string) {
	deviceName := makeDeviceName(ip, port)

	// TODO: This is potentially how we can register devices using built-in Edgex discovery
	// TODO: 	process. There are some questions around it, especially concerning which device
	// TODO: 	profile will be used.
	//driver.deviceCh <- []dsModels.DiscoveredDevice{{
	//	Name:        deviceName,
	//	Protocols:   map[string]edgexModels.ProtocolProperties{
	//		"tcp": {
	//			"host": ip,
	//			"port": port,
	//		},
	//	},
	//	Description: "LLRP RFID Reader",
	//	Labels:      nil,
	//},
	//}

	if _, err := sdk.RunningService().GetDeviceByName(deviceName); err == nil {
		// if err is nil, device already exists
		driver.lc.Info("Device already exists, not registering", "deviceId", deviceName, "profile", profileName)
		return
	}

	driver.lc.Info("Device not found in EdgeX database. Now Registering.", "deviceId", deviceName, "profile", profileName)
	_, err := sdk.RunningService().AddDevice(edgexModels.Device{
		Name:           deviceName,
		AdminState:     edgexModels.Unlocked,
		OperatingState: edgexModels.Enabled,
		Protocols: map[string]edgexModels.ProtocolProperties{
			"tcp": {
				"host": ip,
				"port": port,
			},
		},
		Profile: edgexModels.DeviceProfile{
			Name: profileName,
		},
	})
	if err != nil {
		driver.lc.Error("Device registration failed",
			"device", deviceName, "profile", profileName, "cause", err)
	}
}
