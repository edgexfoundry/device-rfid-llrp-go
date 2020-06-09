package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	sdk "github.com/edgexfoundry/device-sdk-go/pkg/service"
	log "github.com/sirupsen/logrus"
	"github.com/zmap/zgrab2"
	"net"
	"sync"
	"time"
)

// todo: most of these need to be configurable
const (
	probeTimeout          = 1 * time.Second
	llrpPort              = 5084
	llrpPortStr           = "5084" // todo: support TLS connections
	scanVirtualInterfaces = false
	profileName           = "Device.LLRP.Profile"

	llrpProtocol = "llrp"
)

// virtualRegex is a regular expression to determine if an interface is likely to be a virtual interface
var virtualRegex = regexp.MustCompile("^(?:docker[0-9]+|br-.*|virbr[0-9]+.*|docker_gwbridge|veth.*)$")

func init() {
	RegisterModule()
}

// ScanResults is the output of the scan when successful.
type ScanResults struct {
	Version uint8 `json:"version,omitempty"`
}

type Flags struct {
	zgrab2.BaseFlags
	ScanVirtualInterfaces bool
}

// Module implements the zgrab2.Module interface.
type Module struct {
}

// Scanner implements the zgrab2.Scanner interface, and holds the state for a single scan.
type Scanner struct {
	config *Flags
}

// Connection holds the state for a single connection to the LLRP reader (during a scan).
type Connection struct {
	config  *Flags
	results ScanResults
	conn    net.Conn
}

// RegisterModule registers the llrp zgrab2 module.
func RegisterModule() {
	var module Module
	_, err := zgrab2.AddCommand(llrpProtocol, "LLRP", module.Description(), llrpPort, &module)
	if err != nil {
		log.Fatal(err)
	}
}

// NewFlags returns the default flags object to be filled in with the
// command-line arguments.
func (m *Module) NewFlags() interface{} {
	return new(Flags)
}

// NewScanner returns a new Scanner instance.
func (m *Module) NewScanner() zgrab2.Scanner {
	return new(Scanner)
}

// Description returns an overview of this module.
func (m *Module) Description() string {
	return "Scan for LLRP RFID readers"
}

func (f *Flags) Validate(args []string) (err error) {
	return nil
}
func (f *Flags) Help() string {
	return ""
}
func (s *Scanner) InitPerSender(senderID int) error {
	return nil
}

// Protocol returns the llrpProtocol identifier for the scanner.
func (s *Scanner) Protocol() string {
	return llrpProtocol
}

// Init initializes the Scanner instance with the flags from the command line.
func (s *Scanner) Init(flags zgrab2.ScanFlags) error {
	f, _ := flags.(*Flags)
	s.config = f
	return nil
}

// GetName returns the configured name for the Scanner.
func (s *Scanner) GetName() string {
	return s.config.Name
}

// GetTrigger returns the Trigger defined in the Flags.
func (s *Scanner) GetTrigger() string {
	return s.config.Trigger
}

// readResponse reads a response chunk from the reader
func (llrp *Connection) readResponse() (uint8, error) {
	buf := make([]byte, headerSz)
	_, err := llrp.conn.Read(buf)
	if err != nil {
		log.Error(err)
		return 0, err
	}

	connStatus := header{}
	err = connStatus.UnmarshalBinary(buf)
	log.Debugf("connection header: %+v", connStatus)

	if connStatus.typ != ReaderEventNotification {
		return 0, errors.New("wrong connection status")
	}

	return connStatus.version, nil
}

// Scan probes a single ScanTarget to determine if it is an LLRP reader
func (s *Scanner) Scan(t zgrab2.ScanTarget) (status zgrab2.ScanStatus, result interface{}, thrown error) {
	var err error
	conn, err := t.Open(&s.config.BaseFlags)
	if err != nil {
		return zgrab2.TryGetScanStatus(err), nil, err
	}
	defer conn.Close()

	llrpConn := Connection{conn: conn, config: s.config, results: ScanResults{}}
	ret, err := llrpConn.readResponse()
	if err != nil {
		return zgrab2.TryGetScanStatus(err), nil, err
	}
	llrpConn.results.Version = ret
	log.Debugf("llrpConn.results: %+v", llrpConn.results)

	return zgrab2.SCAN_SUCCESS, &llrpConn.results, nil
}

func scanOutputHandler(results <-chan []byte) error {
	var err error
	for result := range results {
		grab := new(zgrab2.Grab)
		err = json.Unmarshal(result, grab)
		if err != nil {
			log.Error(err)
		} else {
			// note: need to use llrpProtocol NOT scanName
			if grab.Data[llrpProtocol].Status == zgrab2.SCAN_SUCCESS {
				log.Infof("%+v", grab.Data[llrpProtocol])
				registerDeviceIfNeeded(grab.IP, llrpPortStr)
			}
		}
	}
	return nil
}

func scanTargetGenerator(flags Flags) zgrab2.InputTargetsFunc {
	return func(ch chan<- zgrab2.ScanTarget) error {
		nets, err := getIPv4Nets(flags.ScanVirtualInterfaces)
		if err != nil {
			return err
		}

		// create a device name lookup table. this is to avoid making calls to the sdk directly
		// because the sdk logs every missed device lookup
		devices := sdk.RunningService().Devices()
		deviceMap := make(map[string]bool, len(devices))
		for _, d := range devices {
			deviceMap[d.Name] = true
		}

		var ip net.IP
		for _, ipnet := range nets {
			if ipnet.Mask != nil {
				// add a target for every IP in the CIDR block
				for ip = ipnet.IP.Mask(ipnet.Mask); ipnet.Contains(ip); incrementIP(ip) {
					if _, found := deviceMap[makeDeviceName(ip.String(), llrpPortStr)]; found {
						log.Infof("Skip scan of %s, device already registered", ip.String())
						continue
					}

					clone := make([]byte, len(ip))
					copy(clone, ip)
					ch <- zgrab2.ScanTarget{IP: clone, Domain: "", Tag: ""}
				}
			} else {
				ch <- zgrab2.ScanTarget{IP: ip, Domain: "", Tag: ""}
			}
		}
		return nil
	}
}

func RunScanner() (*zgrab2.Monitor, *sync.WaitGroup, error) {
	var mod Module
	scanner := mod.NewScanner()
	flags := Flags{
		BaseFlags: zgrab2.BaseFlags{
			Port:           llrpPort,
			Name:           llrpProtocol,
			Timeout:        probeTimeout,
			BytesReadLimit: 1024,
		},
		ScanVirtualInterfaces: scanVirtualInterfaces,
	}

	// for some reason this appears to be the only way for the default
	// values to be loaded and validated
	_, _, _, err := zgrab2.ParseCommandLine([]string{llrpProtocol})
	if err != nil {
		driver.lc.Error(fmt.Sprintf("error configuring zgrab2: %v", err))
		return nil, nil, err
	}

	scanName := llrpProtocol + strconv.FormatInt(time.Now().UnixNano(), 10)
	zgrab2.SetInputFunc(scanTargetGenerator(flags))
	zgrab2.SetOutputFunc(scanOutputHandler)

	err = scanner.Init(&flags)
	if err != nil {
		driver.lc.Error(fmt.Sprintf("error initializing scanner: %v", err))
		return nil, nil, err
	}

	zgrab2.RegisterScan(scanName, scanner)

	wg := sync.WaitGroup{}
	monitor := zgrab2.MakeMonitor(1, &wg)
	zgrab2.Process(monitor)
	return monitor, &wg, nil
}
