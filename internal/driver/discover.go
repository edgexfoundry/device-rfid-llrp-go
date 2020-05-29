package driver

import (
	"fmt"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"net"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// todo: most of these need to be configurable
const (
	probeTimeout          = 10 * time.Second
	llrpPort              = 5084 // todo: support TLS connections?
	probeAsyncLimit       = 100
	scanVirtualInterfaces = false
	ipv4Length = 4
)

// virtualRegex is a regular expression to determine if an interface is likely to be a virtual interface
var virtualRegex = regexp.MustCompile("^(?:docker[0-9]+|br-.*|virbr[0-9]+.*|docker_gwbridge|veth.*)$")

var ErrDiscoverClosed = errors.New("Discover closed")
var ErrNoIPs = errors.New("No valid IPs")
var ErrInvalidIPv4 = errors.New("Not a valid IPv4")

// discoverState keeps track of the discovery process with handles to manage it over time
type discoverState struct {
	wg sync.WaitGroup

	readersMu sync.Mutex
	readers   []*Reader

	isClosed uint32        // used atomically to prevent duplicate closure of done
	ipChan   chan net.IP   // channel of ips to scan
}

func newDiscoverState() *discoverState {
	return &discoverState{
		ipChan: make(chan net.IP),
	}
}

// Discover probes all addresses in the local network to attempt to discover any possible
// RFID readers that support LLRP. It returns pointers to the discovered Readers.
func Discover() ([]*Reader, error) {
	ds := newDiscoverState()

	nets, err := getIPv4Nets(scanVirtualInterfaces)
	if err != nil {
		return nil, err
	}
	log.Infof("%+v", nets)

	// start this before adding any ips so they are ready to process
	for i := 0; i < probeAsyncLimit; i++ {
		go ds.ipWorker()
	}

	for _, inet := range nets {
		ips, err := expandIPNet(inet)
		if err != nil {
			// todo: should this warn and continue to allow partial scans?
			return nil, err
		}
		log.Infof("ips: %v", len(ips))

		for _, ip := range ips {
			ip := ip
			ds.wg.Add(1)
			ds.ipChan <- ip
		}
	}

	log.Debug("waiting for wait group")
	ds.wg.Wait()
	err = ds.close()
	if err != nil {
		log.Warnf("error closing discovery: %v", err)
	}
	log.Debug("wait group completed")

	return ds.readers, nil
}

// close atomically closes the discoverState
func (ds *discoverState) close() error {
	if atomic.CompareAndSwapUint32(&ds.isClosed, 0, 1) {
		close(ds.ipChan)
		return nil
	} else {
		return ErrDiscoverClosed
	}
}

// ipWorker listens to the discoverState's ipChan for incoming ip addresses
// and asynchronously probes each IP to determine if it is an LLRP reader. The amount of
// simultaneous go routines can be scaled using `probeLimit`.
func (ds *discoverState) ipWorker() {
	for ip := range ds.ipChan {
		r, err := ds.probe(ip, llrpPort)
		if err == nil && r != nil {
			ds.readersMu.Lock()
			ds.readers = append(ds.readers, r)
			ds.readersMu.Unlock()
		}
		ds.wg.Done()
	}
}

// probe attempts to make a connection to a specific ip and port to determine
// if an LLRP reader exists at that network address
func (ds *discoverState) probe(ip net.IP, port int) (*Reader, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip.String(), port), probeTimeout)
	if err != nil {
		return nil, err
	}

	log.Info("connection dialed")
	r, err := NewReader(WithConn(conn))
	if err != nil {
		return nil, err
	}

	log.Debug("run negotiation")
	err = r.negotiate()
	if err != nil {
		return nil, err
	}

	log.Infof("Reader connection successfully negotiated @ %v:%d!", ip, port)

	return r, nil
}

// expandIPNet will take an IPNet (ip and mask combo) and convert it to a slice of
// all the valid ip addresses in that network.
// Example: 192.168.1.33/24 -> []net.IP{192.168.1.1, ..., 192.168.1.254}
func expandIPNet(inet *net.IPNet) ([]net.IP, error) {
	var ips []net.IP
	for ip := inet.IP.Mask(inet.Mask); inet.Contains(ip); _ = incrementIPv4(ip) {
		tmp := make([]byte, len(ip))
		copy(tmp, ip)
		ips = append(ips, tmp)
	}

	if ips == nil {
		return nil, ErrNoIPs
	}

	if len(ips) <= 2 {
		// in the case of a /32 or /31 subnet, there will only be 1 IP
		return []net.IP{inet.IP}, nil
	}

	// remove the first and last IP addresses in the range,
	// which are reserved as the network address and broadcast address respectively
	return ips[1 : len(ips)-1], nil
}

// incrementIPv4 will increment an ip address to the next viable address
// IP addresses are stored as a 4 byte slice so 127.0.0.1 equals []byte{127, 0, 0, 1}
// it starts at the last byte and increments it. because a byte is unsigned, it will wrap at 255 back to 0.
// when it overflows to 0 it will continue to the next higher level byte, otherwise it will stop incrementing
func incrementIPv4(ip net.IP) error {
	if len(ip) != ipv4Length {
		return ErrInvalidIPv4
	}

	// iterate over the bytes of the ip backwards
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++

		if ip[i] > 0 {
			// no overflow has occurred, we are done
			return nil
		}
	}

	return nil
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
			log.Infof("Skipping virtual network interface: %v", iface.Name)
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

				log.Info(iface.Name)
				log.Info(v)
				nets = append(nets, v)
			}
		}
	}

	return nets, nil
}

func RegisterReader() {
	// todo
}
