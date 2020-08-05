//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	edgexModels "github.com/edgexfoundry/go-mod-core-contracts/models"
	"github.com/pkg/errors"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
	"math/bits"
	"net"
	"sync"
	"time"
)

const (
	DefaultDevicePrefix = "LLRP"
	UnknownVendorID     = 0
	UnknownModelID      = 0
)

// discoveryInfo holds information about a discovered device
type discoveryInfo struct {
	deviceName string
	host       string
	port       string
	vendor     uint32
	model      uint32
}

// newDiscoveryInfo creates a new discoveryInfo with just a host and port pre-filled
func newDiscoveryInfo(host string, port string) *discoveryInfo {
	return &discoveryInfo{
		host: host,
		port: port,
	}
}

// workerParams is a helper struct to store shared parameters to ipWorkers
type workerParams struct {
	deviceMap map[string]bool
	ipCh      <-chan uint32
	resultCh  chan<- *discoveryInfo
}

// autoDiscover probes all addresses in the configured network to attempt to discover any possible
// RFID readers that support LLRP.
func autoDiscover() (discovered []dsModels.DiscoveredDevice) {
	ipCh := make(chan uint32, 5*driver.config.ProbeAsyncLimit)
	resultCh := make(chan *discoveryInfo)

	// todo: take in a context and allow for cancellation

	wParams := workerParams{
		deviceMap: makeDeviceMap(),
		ipCh:      ipCh,
		resultCh:  resultCh,
	}

	// start the workers before adding any ips so they are ready to process
	var wgIPWorkers sync.WaitGroup
	wgIPWorkers.Add(driver.config.ProbeAsyncLimit)
	for i := 0; i < driver.config.ProbeAsyncLimit; i++ {
		go func() {
			defer wgIPWorkers.Done()
			ipWorker(&wParams)
		}()
	}

	go func() {
		var wgIPGenerators sync.WaitGroup
		for _, cidr := range driver.config.DiscoverySubnets {
			if cidr == "" {
				driver.lc.Warn("empty CIDR provided, unable to scan for LLRP readers")
				continue
			}

			ip, ipnet, err := net.ParseCIDR(cidr)
			if err != nil {
				driver.lc.Error(fmt.Sprintf("unable to parse CIDR: %s", cidr))
				continue
			}
			if ip == nil || ipnet == nil || ip.To4() == nil {
				driver.lc.Error("currently only ipv4 subnets are supported", "subnet", cidr)
				continue
			}

			// wait on each ipGenerator
			wgIPGenerators.Add(1)
			go func(inet *net.IPNet) {
				defer wgIPGenerators.Done()
				ipGenerator(inet, ipCh)
			}(ipnet)
		}

		// wait for all ip generators to finish, then we can close the ip channel
		wgIPGenerators.Wait()
		close(ipCh)

		// wait for the ipWorkers to finish, then close the results channel which
		// will let the enclosing function finish
		wgIPWorkers.Wait()
		close(resultCh)
	}()

	discovered = make([]dsModels.DiscoveredDevice, 0)
	for c := range resultCh {
		if c != nil {
			discovered = append(discovered, newDiscoveredDevice(c))
		}
	}
	return discovered
}

// makeDeviceMap creates a lookup table of existing devices in order to skip scanning
func makeDeviceMap() map[string]bool {
	devices := driver.svc.Devices()
	deviceMap := make(map[string]bool, len(devices))

	for _, d := range devices {
		if d.Profile.Name != LLRPDeviceProfile {
			// not an llrp reader, skip
			continue
		}

		tcp := d.Protocols["tcp"]
		if tcp == nil {
			driver.lc.Warn("found registered device without tcp protocol information: " + d.Name)
			continue
		}

		if tcp["host"] == "" || tcp["port"] == "" {
			driver.lc.Warn(fmt.Sprintf("registered device is missing required tcp protocol information host: %s, port: %s", tcp["host"], tcp["port"]))
			continue
		}

		deviceMap[tcp["host"]+":"+tcp["port"]] = true
	}

	return deviceMap
}

// ipGenerator generates all valid IP addresses for a given subnet, and
// sends them to the ip channel one at a time
func ipGenerator(inet *net.IPNet, ipCh chan<- uint32) {
	addr := inet.IP.To4()
	if addr == nil {
		return
	}

	mask := inet.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	} else if len(mask) != net.IPv4len {
		return
	}

	umask := binary.BigEndian.Uint32(mask)
	maskSz := bits.OnesCount32(umask)
	if maskSz <= 1 {
		return // skip point-to-point connections
	} else if maskSz >= 31 {
		ipCh <- binary.BigEndian.Uint32(inet.IP)
		return
	}

	netId := binary.BigEndian.Uint32(addr) & umask // network ID
	bcast := netId ^ (^umask)
	for ip := netId + 1; ip < bcast; ip++ {
		if netId&umask != ip&umask {
			continue
		}
		ipCh <- ip
	}
}

// probe attempts to make a connection to a specific ip and port to determine
// if an LLRP reader exists at that network address
func probe(host string, port string, timeout time.Duration) (*discoveryInfo, error) {
	addr := host + ":" + port
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	driver.lc.Info("connection dialed")
	c := llrp.NewClient(llrp.WithLogger(&edgexLLRPClientLogger{
		devName: "probe-" + host,
		lc:      driver.lc,
	}))

	readerConfig := llrp.GetReaderConfigResponse{}
	readerCaps := llrp.GetReaderCapabilitiesResponse{}

	// do message passing in a separate thread
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()

		defer func() {
			if err := c.Shutdown(ctx); err != nil {
				driver.lc.Warn("error occurred while attempting to shutdown temporary discover device: " + err.Error())
			}
		}()

		driver.lc.Debug("sending GetReaderConfig")
		configReq := llrp.GetReaderConfig{
			RequestedData: llrp.ReaderConfReqIdentification,
		}
		err = c.SendFor(ctx, &configReq, &readerConfig)
		if err != nil {
			driver.lc.Error(errors.Wrap(err, "error sending GetReaderConfig").Error())
			return
		}

		driver.lc.Debug("sending GetReaderCapabilities")
		capabilitiesReq := llrp.GetReaderCapabilities{
			ReaderCapabilitiesRequestedData: llrp.ReaderCapGeneralDeviceCapabilities,
		}
		err = c.SendFor(ctx, &capabilitiesReq, &readerCaps)
		if err != nil {
			driver.lc.Error(errors.Wrap(err, "error sending GetReaderCapabilities").Error())
			return
		}
	}()

	driver.lc.Info("connecting to device")
	// this will block until `c.Shutdown()` is called in the above go routine
	if err = c.Connect(conn); !errors.Is(err, llrp.ErrClientClosed) {
		driver.lc.Error("error attempting to connect to potential LLRP device: " + err.Error())
		return nil, err
	}
	driver.lc.Info("connection initiated successfully")

	info := newDiscoveryInfo(host, port)
	if readerCaps.GeneralDeviceCapabilities == nil {
		driver.lc.Warn("readerCaps.GeneralDeviceCapabilities was nil, unable to determine vendor and model info")
		info.vendor = UnknownVendorID
		info.model = UnknownModelID
	} else {
		info.vendor = readerCaps.GeneralDeviceCapabilities.DeviceManufacturer
		info.model = readerCaps.GeneralDeviceCapabilities.Model
	}

	if readerConfig.Identification == nil {
		driver.lc.Error("readerConfig.Identification was nil, unable to register device")
		return nil, fmt.Errorf("unable to retrieve device identification")
	}

	prefix := DefaultDevicePrefix
	if VendorIDType(info.vendor) == Impinj {
		prefix = ImpinjModelType(info.model).HostnamePrefix()
	}

	var suffix string
	rID := readerConfig.Identification.ReaderID
	if readerConfig.Identification.IDType == llrp.ID_MAC_EUI64 && len(rID) >= 3 {
		mac := rID[len(rID)-3:]
		suffix = fmt.Sprintf("%02X-%02X-%02X", mac[0], mac[1], mac[2])
	} else {
		suffix = hex.EncodeToString(rID)
	}

	info.deviceName = prefix + "-" + suffix
	driver.lc.Info(fmt.Sprintf("discovered device: %+v", info))

	return info, nil
}

// ipWorker pulls uint32s, convert to IPs, and sends back successful probes to the resultCh
func ipWorker(params *workerParams) {
	ip := net.IP([]byte{0, 0, 0, 0})
	timeout := time.Duration(driver.config.ProbeTimeoutSeconds) * time.Second

	for a := range params.ipCh {
		binary.BigEndian.PutUint32(ip, a)

		ipStr := ip.String()
		addr := ipStr + ":" + driver.config.ScanPort
		if _, found := params.deviceMap[addr]; found {
			driver.lc.Debug("skip scan of " + addr + ", device already registered")
			continue
		}

		if info, err := probe(ipStr, driver.config.ScanPort, timeout); err == nil && info != nil {
			params.resultCh <- info
		}
	}
}

// newDiscoveredDevice takes the host and port number of a discovered LLRP reader and prepares it for
// registration with EdgeX
func newDiscoveredDevice(info *discoveryInfo) dsModels.DiscoveredDevice {
	labels := []string{
		"RFID",
		"LLRP",
	}
	if VendorIDType(info.vendor) == Impinj {
		labels = append(labels, Impinj.String())
		labels = append(labels, ImpinjModelType(info.model).String())
	}

	return dsModels.DiscoveredDevice{
		Name: info.deviceName,
		Protocols: map[string]edgexModels.ProtocolProperties{
			"tcp": {
				"host": info.host,
				"port": info.port,
			},
		},
		Description: "LLRP RFID Reader",
		Labels:      labels,
	}
}
