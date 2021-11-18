//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/edgexfoundry/device-rfid-llrp-go/internal/llrp"
	dsModels "github.com/edgexfoundry/device-sdk-go/v2/pkg/models"
	"github.com/edgexfoundry/device-sdk-go/v2/pkg/service"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/clients/logger"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/common"
	"github.com/edgexfoundry/go-mod-core-contracts/v2/dtos"
	contract "github.com/edgexfoundry/go-mod-core-contracts/v2/models"
	"github.com/pkg/errors"
)

const (
	ServiceName = "device-rfid-llrp"

	ResourceReaderCap          = "ReaderCapabilities"
	ResourceReaderConfig       = "ReaderConfig"
	ResourceReaderNotification = "ReaderEventNotification"
	ResourceROSpec             = "ROSpec"
	ResourceROSpecID           = "ROSpecID"
	ResourceAccessSpec         = "AccessSpec"
	ResourceAccessSpecID       = "AccessSpecID"
	ResourceROAccessReport     = "ROAccessReport"

	ResourceAction = "Action"
	ActionDelete   = "Delete"
	ActionEnable   = "Enable"
	ActionDisable  = "Disable"
	ActionStart    = "Start"
	ActionStop     = "Stop"
	AttribVendor   = "vendor"
	AttribSubtype  = "subtype"

	// enable this by default, otherwise discovery will not work.
	registerProvisionWatchers = true
	provisionWatcherFolder    = "res/provision_watchers"

	// discoverDebounceDuration is the amount of time to wait for additional changes to discover
	// configuration before auto-triggering a discovery
	discoverDebounceDuration = 10 * time.Second
)

var (
	createOnce sync.Once
	driver     *Driver
)

type protocolMap = map[string]contract.ProtocolProperties

// Driver manages a collection of devices that speak LLRP
// and connects them to the EdgeX ecosystem.
//
// A driver must be initialized before use.
// This is typically done by the Device Service SDK.
// This package maintains a package-global variable
// which it exports via driver.Instance.
//
// The Driver's exported methods are safe for concurrent use.
type Driver struct {
	lc       logger.LoggingClient
	asyncCh  chan<- *dsModels.AsyncValues
	deviceCh chan<- []dsModels.DiscoveredDevice

	done chan struct{}

	activeDevices map[string]*LLRPDevice
	devicesMu     sync.RWMutex

	config   *ServiceConfig
	configMu sync.RWMutex

	addedWatchers bool
	watchersMu    sync.Mutex

	svc ServiceWrapper

	// debounceTimer and debounceMu keep track of when to fire a debounced discovery call
	debounceTimer *time.Timer
	debounceMu    sync.Mutex
}

type MultiErr []error

//goland:noinspection GoReceiverNames
func (me MultiErr) Error() string {
	strs := make([]string, len(me))
	for i, s := range me {
		strs[i] = s.Error()
	}

	return strings.Join(strs, "; ")
}

// EdgeX's Device SDK takes an interface{}
// and uses a runtime-check to determine that it implements ProtocolDriver,
// at which point it will abruptly exit without a panic.
// This restores type-safety by making it so we can't compile
// unless we meet the runtime-required interface.
var _ dsModels.ProtocolDriver = (*Driver)(nil)

// Instance returns the package-global Driver instance, creating it if necessary.
// It must be initialized before use via its Initialize method.
func Instance() *Driver {
	createOnce.Do(func() {
		driver = &Driver{
			activeDevices: make(map[string]*LLRPDevice),
			done:          make(chan struct{}),
		}
	})
	return driver
}

// Initialize performs protocol-specific initialization for the device
// service.
func (d *Driver) Initialize(lc logger.LoggingClient, asyncCh chan<- *dsModels.AsyncValues, deviceCh chan<- []dsModels.DiscoveredDevice) error {
	if lc == nil {
		// prevent panics from this annoyance
		d.lc = logger.NewClient(ServiceName, "DEBUG")
		d.lc.Error("EdgeX initialized us with a nil logger >:(")
	} else {
		d.lc = lc
	}

	d.asyncCh = asyncCh
	d.deviceCh = deviceCh
	d.svc = &DeviceSDKService{
		DeviceService: service.RunningService(),
		lc:            lc,
	}

	d.config = &ServiceConfig{}

	err := d.svc.LoadCustomConfig(d.config, "AppCustom")
	if err != nil {
		return errors.Wrap(err, "custom driver configuration failed to load")
	}

	lc.Debugf("Custom config is : %v", d.config)

	err = d.svc.ListenForCustomConfigChanges(&d.config.AppCustom, "AppCustom", d.updateWritableConfig)
	if err != nil {
		return errors.Wrap(err, "failed to listen to custom config changes")
	}

	registered := d.svc.Devices()
	d.devicesMu.Lock()
	defer d.devicesMu.Unlock()
	for i := range registered {
		device := &registered[i] // the Device struct is nearly 1kb, so this avoids copying it

		addr, err := getAddr(device.Protocols)
		if err != nil {
			d.lc.Error("Unsupported protocol mapping.",
				"error", err,
				"protocols", fmt.Sprintf("%v", device.Protocols),
				"deviceName", device.Name)
			continue
		}

		d.lc.Info("Creating a new Reader connection.", "deviceName", device.Name)
		d.activeDevices[device.Name] = d.NewLLRPDevice(device.Name, addr, device.OperatingState)
	}

	return nil
}

func (d *Driver) updateWritableConfig(rawWritableConfig interface{}) {
	updated, ok := rawWritableConfig.(*CustomConfig)
	if !ok {
		d.lc.Error("unable to update writable custom config: Can not cast raw config to type 'CustomConfig'")
		return
	}

	d.configMu.Lock()
	oldSubnets := d.config.AppCustom.DiscoverySubnets
	oldScanPort := d.config.AppCustom.ScanPort
	d.config.AppCustom = *updated
	d.configMu.Unlock()

	if updated.DiscoverySubnets != oldSubnets || updated.ScanPort != oldScanPort {
		d.lc.Info("discover configuration has changed!")
		d.debouncedDiscover()
	}
}

// HandleReadCommands triggers a protocol Read operation for the specified device.
func (d *Driver) HandleReadCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest) ([]*dsModels.CommandValue, error) {
	d.lc.Debug(fmt.Sprintf("LLRP-Driver.HandleReadCommands: "+
		"device: %s protocols: %v reqs: %+v", devName, p, reqs))

	results, err := d.handleReadCommands(devName, p, reqs)
	if err != nil {
		d.lc.Error("ReadCommands failed.",
			"device", devName,
			"error", err,
			"requests", fmt.Sprintf("%+v", reqs))
	}
	return results, err
}

func (d *Driver) handleReadCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest) ([]*dsModels.CommandValue, error) {
	if len(reqs) == 0 {
		return nil, errors.New("missing requests")
	}

	dev, _, err := d.getDevice(devName, p)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	var responses = make([]*dsModels.CommandValue, len(reqs))
	for i := range reqs {
		var llrpReq llrp.Outgoing
		var llrpResp llrp.Incoming

		switch reqs[i].DeviceResourceName {
		default:
			return nil, errors.Errorf("unknown resource type: %q", reqs[i].DeviceResourceName)
		case ResourceReaderConfig:
			llrpReq = &llrp.GetReaderConfig{}
			llrpResp = &llrp.GetReaderConfigResponse{}
		case ResourceReaderCap:
			llrpReq = &llrp.GetReaderCapabilities{}
			llrpResp = &llrp.GetReaderCapabilitiesResponse{}
		case ResourceROSpec:
			llrpReq = &llrp.GetROSpecs{}
			llrpResp = &llrp.GetROSpecsResponse{}
		case ResourceAccessSpec:
			llrpReq = &llrp.GetAccessSpecs{}
			llrpResp = &llrp.GetAccessSpecsResponse{}
		}

		if err := dev.TrySend(ctx, llrpReq, llrpResp); err != nil {
			return nil, err
		}

		respData, err := json.Marshal(llrpResp)
		if err != nil {
			return nil, err
		}

		cmdValue, err := dsModels.NewCommandValueWithOrigin(reqs[i].DeviceResourceName, common.ValueTypeString, string(respData), time.Now().UnixNano())
		if err != nil {
			return nil, errors.Errorf("Failed to create new command value with origin: %s", err.Error())
		}

		responses[i] = cmdValue

	}

	return responses, nil
}

// HandleWriteCommands passes a slice of CommandRequest struct each representing
// a ResourceOperation for a specific device resource.
// Since the commands are actuation commands, params provide parameters for the individual
// command.
func (d *Driver) HandleWriteCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest, params []*dsModels.CommandValue) error {
	d.lc.Debug(fmt.Sprintf("LLRP-Driver.HandleWriteCommands: "+
		"device: %s protocols: %v reqs: %+v", devName, p, reqs))

	// kinda surprised EdgeX doesn't do this automatically.
	err := d.handleWriteCommands(devName, p, reqs, params)
	if err != nil {
		d.lc.Error("Write Command failed",
			"device", devName,
			"error", err.Error())
	}
	return err
}

func (d *Driver) handleWriteCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest, params []*dsModels.CommandValue) error {
	if len(reqs) == 0 {
		return errors.New("missing requests")
	}

	if len(reqs) != len(params) {
		return errors.Errorf("mismatched command requests and parameters: %d != %d", len(reqs), len(params))
	}

	dev, _, err := d.getDevice(devName, p)
	if err != nil {
		return err
	}

	getAttrib := func(idx int, key string) (interface{}, error) {
		m := reqs[idx].Attributes
		val := m[key]
		if val == "" {
			return "", errors.Errorf("missing custom parameter attribute: %s", key)
		}
		return val, nil
	}

	getUintAttrib := func(req int, key string) (uint64, error) {
		v, err := getAttrib(req, key)
		if err != nil {
			return 0, err
		}

		u, ok := v.(uint64)
		if !ok {
			return 0, fmt.Errorf("unable to cast attribute %q with val %q as uint", key, v)
		}

		return u, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	var llrpReq llrp.Outgoing  // the message to send
	var llrpResp llrp.Incoming // the expected response
	var reqData []byte         // incoming JSON request data, if present
	var dataTarget interface{} // used if the reqData in a subfield of the llrpReq

	switch reqs[0].DeviceResourceName {
	default:
		// assume the resource requires sending a CustomMessage
		customName := reqs[0].DeviceResourceName
		vendor, err := getUintAttrib(0, AttribVendor)
		if err != nil {
			return err
		}

		if vendor > uint64(math.MaxUint32) {
			return errors.Errorf("resource %q vendor PEN %d exceeds uint32", customName, vendor)
		}

		subtype, err := getUintAttrib(0, AttribSubtype)
		if err != nil {
			return err
		}

		if subtype > uint64(math.MaxUint8) {
			return errors.Errorf("resource %q message subtype %d exceeds uint8", customName, subtype)
		}

		b64payload, err := params[0].StringValue()
		if err != nil {
			return err
		}

		valData, err := base64.StdEncoding.DecodeString(b64payload)
		if err != nil {
			return errors.Errorf("unable to base64 decode attribute value for %q", customName)
		}

		llrpReq = &llrp.CustomMessage{
			VendorID:       uint32(vendor),
			MessageSubtype: uint8(subtype),
			Data:           valData,
		}
		llrpResp = &llrp.CustomMessage{}

	case ResourceReaderConfig:
		data, err := params[0].StringValue()
		if err != nil {
			return err
		}

		reqData = []byte(data)
		llrpReq = &llrp.SetReaderConfig{}
		llrpResp = &llrp.SetReaderConfigResponse{}
	case ResourceROSpec:
		data, err := params[0].StringValue()
		if err != nil {
			return errors.Wrap(err, "unable to get ROSpec parameter")
		}

		reqData = []byte(data)
		addSpec := llrp.AddROSpec{}
		dataTarget = &addSpec.ROSpec // the incoming data is an ROSpec, not AddROSpec
		llrpReq = &addSpec           // but we want to send AddROSpec, not just ROSpec
		llrpResp = &llrp.AddROSpecResponse{}

	case ResourceROSpecID:
		if len(params) != 2 {
			return errors.Errorf("expected 2 resources for ROSpecID op, but got %d", len(params))
		}

		if params[1].DeviceResourceName != ResourceAction {
			return errors.Errorf("expected Action resource with ROSpecID, but got %q",
				params[1].DeviceResourceName)
		}

		roID, err := params[0].Uint32Value()
		if err != nil {
			return errors.Wrap(err, "failed to get access spec ID")
		}

		action, err := params[1].StringValue()
		if err != nil {
			return err
		}

		switch action {
		default:
			return errors.Errorf("unknown ROSpecID action: %q", action)
		case ActionEnable:
			llrpReq = &llrp.EnableROSpec{ROSpecID: roID}
			llrpResp = &llrp.EnableROSpecResponse{}
		case ActionStart:
			llrpReq = &llrp.StartROSpec{ROSpecID: roID}
			llrpResp = &llrp.StartROSpecResponse{}
		case ActionStop:
			llrpReq = &llrp.StopROSpec{ROSpecID: roID}
			llrpResp = &llrp.StopROSpecResponse{}
		case ActionDisable:
			llrpReq = &llrp.DisableROSpec{ROSpecID: roID}
			llrpResp = &llrp.DisableROSpecResponse{}
		case ActionDelete:
			llrpReq = &llrp.DeleteROSpec{ROSpecID: roID}
			llrpResp = &llrp.DeleteROSpecResponse{}
		}

	case ResourceAccessSpecID:
		if len(reqs) != 2 {
			return errors.Errorf("expected 2 resources for AccessSpecID op, but got %d", len(reqs))
		}

		if params[1].DeviceResourceName != ResourceAction {
			return errors.Errorf("expected Action resource with AccessSpecID, but got %q",
				params[1].DeviceResourceName)
		}

		asID, err := params[0].Uint32Value()
		if err != nil {
			return errors.Wrap(err, "failed to get access spec ID")
		}

		action, err := params[1].StringValue()
		if err != nil {
			return err
		}

		switch action {
		default:
			return errors.Errorf("unknown ROSpecID action: %q", action)
		case ActionEnable:
			llrpReq = &llrp.EnableAccessSpec{AccessSpecID: asID}
			llrpResp = &llrp.EnableAccessSpecResponse{}
		case ActionDisable:
			llrpReq = &llrp.DisableAccessSpec{AccessSpecID: asID}
			llrpResp = &llrp.DisableAccessSpecResponse{}
		case ActionDelete:
			llrpReq = &llrp.DeleteAccessSpec{AccessSpecID: asID}
			llrpResp = &llrp.DeleteAccessSpecResponse{}
		}
	}

	if reqData != nil {
		if dataTarget != nil {
			if err := json.Unmarshal(reqData, dataTarget); err != nil {
				return errors.Wrap(err, "failed to unmarshal request")
			}
		} else {
			if err := json.Unmarshal(reqData, llrpReq); err != nil {
				return errors.Wrap(err, "failed to unmarshal request")
			}
		}
	}

	// SendFor will handle turning ErrorMessages and failing LLRPStatuses into errors.
	if err := dev.TrySend(ctx, llrpReq, llrpResp); err != nil {
		return err
	}

	go func(resName, devName string, resp llrp.Incoming) {
		respData, err := json.Marshal(resp)
		if err != nil {
			d.lc.Error("failed to marshal response", "message", resName, "error", err)
			return
		}

		cv, err := dsModels.NewCommandValueWithOrigin(resName, common.ValueTypeString, string(respData), time.Now().UnixNano())
		if err != nil {
			d.lc.Errorf("Failed to create new command value with origin: %s", err.Error())
			return
		}

		d.asyncCh <- &dsModels.AsyncValues{
			DeviceName:    devName,
			CommandValues: []*dsModels.CommandValue{cv},
		}
	}(reqs[0].DeviceResourceName, dev.name, llrpResp)

	return nil
}

// Stop the Driver, causing it to shutdown its active device connections
// and no longer process commands or upstream reports.
//
// If force is false, the Driver attempts to gracefully shutdown active devices
// by sending them a CloseConnection message and waiting a short time for their response.
// If force is true, it immediately closes all active connections.
// In neither case does it tell devices to stop reading.
//
// EdgeX says DeviceServices should close the async readings channel,
// but tracing their code reveals they call close on the channel,
// so doing so would cause a panic.
func (d *Driver) Stop(force bool) error {
	// Then Logging Client might not be initialized
	if d.lc == nil {
		d.lc = logger.NewClient(ServiceName, "DEBUG")
		d.lc.Error("EdgeX called Stop without calling Initialize >:(")
	}
	d.lc.Debug("LLRP-Driver.Stop called", "force", force)

	close(d.done)

	d.devicesMu.Lock()
	defer d.devicesMu.Unlock()

	ctx := context.Background()

	var wg *sync.WaitGroup
	if !force {
		wg = new(sync.WaitGroup)
		wg.Add(len(d.activeDevices))
		defer wg.Wait()

		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, shutdownGrace)
		defer cancel()
	}

	for _, dev := range d.activeDevices {
		go func(dev *LLRPDevice) {
			if err := dev.Stop(ctx); err != nil {
				d.lc.Error("Error attempting client shutdown.", "error", err.Error())
			}
			if !force {
				wg.Done()
			}
		}(dev)
	}

	d.activeDevices = make(map[string]*LLRPDevice)
	return nil
}

// AddDevice tells the driver attempt to actively manage a device.
//
// The Device Service SDK calls this when a new Device
// associated with this Device Service is added,
// so this assumes the device is already registered with EdgeX.
func (d *Driver) AddDevice(deviceName string, protocols protocolMap, adminState contract.AdminState) error {
	d.lc.Debug(fmt.Sprintf("Adding new device: %s protocols: %v adminState: %v",
		deviceName, protocols, adminState))
	_, _, err := d.getDevice(deviceName, protocols)
	if err != nil {
		d.lc.Error("Failed to add device.", "error", err, "deviceName", deviceName)
	}
	return err
}

// UpdateDevice updates a device managed by this Driver.
//
// The Device Service SDK calls it when a Device is updated,
// so this assumes the device is already registered with EdgeX.
//
// If the Driver has a device with this name, but the device's address changes,
// this will shutdown any current connection associated with the named device,
// update the address, and attempt to reconnect at the new address and port.
// If the address is the same, nothing happens.
func (d *Driver) UpdateDevice(deviceName string, protocols protocolMap, adminState contract.AdminState) (err error) {
	d.lc.Debug(fmt.Sprintf("Updating device: %s protocols: %v adminState: %v",
		deviceName, protocols, adminState))
	defer func() {
		if err != nil {
			d.lc.Error("Failed to update device.",
				"error", err, "deviceName", deviceName,
				"protocolMap", fmt.Sprintf("%v", protocols))
		}
	}()

	// This uses the shutdownGrace period because updating the address
	// may require closing a current connection to an existing device.
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	var dev *LLRPDevice
	var isNew bool
	dev, isNew, err = d.getDevice(deviceName, protocols)
	// No need to call update if the device was just created.
	if isNew || err != nil {
		return err
	}

	var addr net.Addr
	addr, err = getAddr(protocols)
	if err != nil {
		return err
	}

	return dev.UpdateAddr(ctx, addr)
}

// RemoveDevice is a callback function that is invoked
// when a Device associated with this Device Service is removed
func (d *Driver) RemoveDevice(deviceName string, p protocolMap) error {
	d.lc.Debug(fmt.Sprintf("Removing device: %s protocols: %v", deviceName, p))

	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	d.removeDevice(ctx, deviceName)
	return nil
}

// getDevice returns an LLRPDevice, creating one if needed.
//
// If the Driver is already managing an LLRPDevice with this name,
// then this call simply returns it.
// Otherwise, it creates and returns a new LLRPDevice instance
// after adding it to its map of managed devices.
// If the new LLRPDevice is created as a result of this call,
// the returned boolean `isNew` will be the true.
func (d *Driver) getDevice(name string, p protocolMap) (dev *LLRPDevice, isNew bool, err error) {
	// Try with just a read lock.
	d.devicesMu.RLock()
	c, ok := d.activeDevices[name]
	d.devicesMu.RUnlock()
	if ok {
		return c, false, nil
	}

	addr, err := getAddr(p)
	if err != nil {
		return nil, false, err
	}

	// It's important it holds the lock while creating a device.
	// If two requests arrive at about the same time and target the same device,
	// one will block waiting for the lock and the other will create/add it.
	// When gaining the lock, we recheck the map.
	// This way, only one device exists for any name,
	// and all requests that target it use the same one.
	d.devicesMu.Lock()
	defer d.devicesMu.Unlock()

	dev, ok = d.activeDevices[name]
	if ok {
		return dev, false, nil
	}

	d.lc.Info("Creating new connection for device.", "device", name)
	dev = d.NewLLRPDevice(name, addr, contract.Up)
	d.activeDevices[name] = dev
	return dev, true, nil
}

// removeDevice deletes a device from the active devices map
// and shuts down its client connection to an LLRP device.
func (d *Driver) removeDevice(ctx context.Context, deviceName string) {
	d.devicesMu.Lock()
	defer d.devicesMu.Unlock()

	if dev, ok := d.activeDevices[deviceName]; ok {
		d.lc.Info("Stopping connection for device.", "device", deviceName)
		if err := dev.Stop(ctx); err != nil {
			d.lc.Error("Error attempting client shutdown.", "error", err.Error())
		}
		delete(d.activeDevices, deviceName)
	}
}

// getAddr extracts an address from a protocol mapping.
//
// It expects the map to have {"tcp": {"host": "<ip>", "port": "<port>"}}.
func getAddr(protocols protocolMap) (net.Addr, error) {
	if protocols == nil {
		return nil, errors.New("protocol map is nil")
	}

	tcpInfo := protocols["tcp"]
	if tcpInfo == nil {
		return nil, errors.New("missing tcp protocol")
	}

	host, port := tcpInfo["host"], tcpInfo["port"]
	if host == "" || port == "" {
		return nil, errors.Errorf("tcp missing host or port (%q, %q)", host, port)
	}

	addr, err := net.ResolveTCPAddr("tcp", host+":"+port)
	return addr, errors.Wrapf(err,
		"unable to create addr for tcp protocol (%q, %q)", host, port)
}

func (d *Driver) addProvisionWatchers() error {
	files, err := ioutil.ReadDir(provisionWatcherFolder)
	if err != nil {
		return err
	}

	d.lc.Debugf("%d provision watcher files found", len(files))

	var errs []error
	for _, file := range files {
		filename := filepath.Join(provisionWatcherFolder, file.Name())
		d.lc.Debugf("processing %s", filename)
		var watcher dtos.ProvisionWatcher
		data, err := ioutil.ReadFile(filename)
		if err != nil {
			errs = append(errs, errors.Wrap(err, "error reading file "+filename))
			continue
		}

		if err := json.Unmarshal(data, &watcher); err != nil {
			errs = append(errs, errors.Wrap(err, "error unmarshalling provision watcher "+filename))
			continue
		}

		err = common.Validate(watcher)
		if err != nil {
			errs = append(errs, errors.Wrap(err, "provision watcher validation failed "+filename))
			continue
		}

		if _, err := d.svc.GetProvisionWatcherByName(watcher.Name); err == nil {
			continue // provision watcher already exists
		}

		watcherModel := dtos.ToProvisionWatcherModel(watcher)

		d.lc.Infof("Adding provision watcher:%s", watcherModel.Name)
		id, err := d.svc.AddProvisionWatcher(watcherModel)
		if err != nil {
			errs = append(errs, errors.Wrap(err, "error adding provision watcher "+watcherModel.Name))
			continue
		}
		d.lc.Infof("Successfully added provision watcher: %s,  ID: %s", watcherModel.Name, id)
	}

	if errs != nil {
		return MultiErr(errs)
	}
	return nil
}

// debouncedDiscover adds or updates a future call to Discover. This function is intended to be
// called by the config watcher in response to any configuration changes related to discovery.
// The reason Discover calls are being debounced is to allow the user to make multiple changes to
// their configuration, and only fire the discovery once.
//
// The way it works is that this code creates and starts a timer for discoverDebounceDuration.
// Every subsequent call to this function before that timer elapses resets the timer to
// discoverDebounceDuration. Once the timer finally elapses, the Discover function is called.
func (d *Driver) debouncedDiscover() {
	d.lc.Debug(fmt.Sprintf("trigger debounced discovery in %v", discoverDebounceDuration))

	// everything in this function is mutex-locked and is safe to access asynchronously
	d.debounceMu.Lock()
	defer d.debounceMu.Unlock()

	if d.debounceTimer != nil {
		// timer is already active, so reset it (debounce)
		d.debounceTimer.Reset(discoverDebounceDuration)
	} else {
		// no timer is active, so create and start a new one
		d.debounceTimer = time.NewTimer(discoverDebounceDuration)

		// asynchronously listen for the timer to elapse. this go routine will only ever be run
		// once due to mutex locking and the above if statement.
		go func() {
			// wait for timer to tick
			<-d.debounceTimer.C

			// remove timer. we must lock the mutex as this go routine runs separately from the
			// outer function's locked scope
			d.debounceMu.Lock()
			d.debounceTimer = nil
			d.debounceMu.Unlock()

			d.Discover()
		}()
	}
}

// Discover performs a discovery of LLRP readers on the network and passes them to EdgeX to get provisioned
func (d *Driver) Discover() {
	d.lc.Info("Discover was called.")

	d.configMu.RLock()
	maxSeconds := driver.config.AppCustom.MaxDiscoverDurationSeconds
	d.configMu.RUnlock()

	if registerProvisionWatchers {
		d.watchersMu.Lock()
		if !d.addedWatchers {
			if err := d.addProvisionWatchers(); err != nil {
				d.lc.Error("Error adding provision watchers. Newly discovered devices may fail to register with EdgeX.",
					"error", err.Error())
				// Do not return on failure, as it is possible there are alternative watchers registered.
				// And if not, the discovered devices will just not be registered with EdgeX, but will
				// still be available for discovery again.
			} else {
				d.addedWatchers = true
			}
		}
		d.watchersMu.Unlock()
	}

	ctx := context.Background()
	if maxSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(),
			time.Duration(maxSeconds)*time.Second)
		defer cancel()
	}

	d.discover(ctx)
}

func (d *Driver) discover(ctx context.Context) {
	d.configMu.RLock()
	params := discoverParams{
		// split the comma separated string here to avoid issues with EdgeX's Consul implementation
		subnets:    strings.Split(d.config.AppCustom.DiscoverySubnets, ","),
		asyncLimit: d.config.AppCustom.ProbeAsyncLimit,
		timeout:    time.Duration(d.config.AppCustom.ProbeTimeoutSeconds) * time.Second,
		scanPort:   d.config.AppCustom.ScanPort,
	}
	d.configMu.RUnlock()

	t1 := time.Now()
	result := autoDiscover(ctx, params)
	if ctx.Err() != nil {
		d.lc.Warn("Discover process has been cancelled!", "ctxErr", ctx.Err())
	}

	d.lc.Info(fmt.Sprintf("Discovered %d new devices in %v.", len(result), time.Now().Sub(t1)))
	// pass the discovered devices to the EdgeX SDK to be passed through to the provision watchers
	d.deviceCh <- result
}
