//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
	"io/ioutil"
	"net"
	"sync"
	"time"

	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
)

const (
	ServiceName string = "edgex-device-llrp"
)

var once sync.Once
var driver *Driver

type Driver struct {
	lc       logger.LoggingClient
	asyncCh  chan<- *dsModels.AsyncValues
	deviceCh chan<- []dsModels.DiscoveredDevice

	clients      map[string]*llrp.Client
	clientsMapMu sync.RWMutex

	svc ServiceWrapper
}

func NewProtocolDriver() dsModels.ProtocolDriver {
	once.Do(func() {
		driver = &Driver{
			clients: make(map[string]*llrp.Client),
		}
	})
	return driver
}

func (d *Driver) service() ServiceWrapper {
	if d.svc == nil {
		d.svc = RunningService()
	}
	return d.svc
}

// Initialize performs protocol-specific initialization for the device
// service.
func (d *Driver) Initialize(lc logger.LoggingClient, asyncCh chan<- *dsModels.AsyncValues, deviceCh chan<- []dsModels.DiscoveredDevice) error {
	d.lc = lc
	d.asyncCh = asyncCh
	d.deviceCh = deviceCh

	go func() {
		// hack: sleep to allow edgex time to finish loading cache and clients
		time.Sleep(5 * time.Second)

		d.addProvisionWatcher()
		// todo: check configuration to make sure discovery is enabled
		d.Discover()
	}()
	return nil
}

type protocolMap = map[string]contract.ProtocolProperties

const (
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
)

// HandleReadCommands triggers a protocol Read operation for the specified device.
func (d *Driver) HandleReadCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest) ([]*dsModels.CommandValue, error) {
	d.lc.Debug(fmt.Sprintf("LLRP-Driver.HandleWriteCommands: "+
		"device: %s protocols: %v reqs: %+v", devName, p, reqs))

	if len(reqs) == 0 {
		return nil, errors.New("missing requests")
	}

	c, err := d.getClient(devName, p)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	var responses = make([]*dsModels.CommandValue, len(reqs))
	for i := range reqs {
		var llrpReq llrp.Outgoing
		var llrpResp llrp.Incoming

		switch reqs[i].DeviceResourceName {
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

		if err := c.SendFor(ctx, llrpReq, llrpResp); err != nil {
			return nil, err
		}

		respData, err := json.Marshal(llrpResp)
		if err != nil {
			return nil, err
		}

		responses[i] = dsModels.NewStringValue(
			reqs[i].DeviceResourceName, time.Now().UnixNano(), string(respData))
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

	if len(reqs) == 0 {
		return errors.New("missing requests")
	}

	c, err := d.getClient(devName, p)
	if err != nil {
		return err
	}

	getParam := func(name string, idx int, key string) (*dsModels.CommandValue, error) {
		if idx > len(params) {
			return nil, errors.Errorf("%s needs at least %d parameters, but got %d",
				name, idx, len(params))
		}

		cv := params[idx]
		if cv == nil {
			return nil, errors.Errorf("%s requires parameter %s", name, key)
		}

		if cv.DeviceResourceName != key {
			return nil, errors.Errorf("%s expected parameter %d: %s, but got %s",
				name, idx, key, cv.DeviceResourceName)
		}

		return cv, nil
	}

	getStrParam := func(name string, idx int, key string) (string, error) {
		if cv, err := getParam(name, idx, key); err != nil {
			return "", err
		} else {
			return cv.StringValue()
		}
	}

	getUint32Param := func(name string, idx int, key string) (uint32, error) {
		if cv, err := getParam(name, idx, key); err != nil {
			return 0, err
		} else {
			return cv.Uint32Value()
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	var llrpReq llrp.Outgoing
	var llrpResp llrp.Incoming

	var reqData []byte // for arbitrary JSON data structures to unmarshal

	switch reqs[0].DeviceResourceName {
	case ResourceReaderConfig:
		data, err := getStrParam("Set"+ResourceReaderConfig, 0, ResourceReaderConfig)
		if err != nil {
			return err
		}

		reqData = []byte(data)
		llrpReq = &llrp.SetReaderConfig{}
		llrpResp = &llrp.SetReaderConfigResponse{}
	case ResourceROSpec:
		data, err := getStrParam("Add"+ResourceROSpec, 0, ResourceROSpec)
		if err != nil {
			return err
		}

		reqData = []byte(data)
		llrpReq = &llrp.AddROSpec{}
		llrpResp = &llrp.AddROSpecResponse{}
	case ResourceROSpecID:
		if len(params) != 2 {
			return errors.Errorf("expected 2 resources for ROSpecID op, but got %d", len(params))
		}

		action, err := getStrParam(ResourceROSpec, 1, ResourceAction)
		if err != nil {
			return err
		}

		roID, err := getUint32Param(action+ResourceROSpec, 0, ResourceROSpecID)
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

		action := reqs[1].DeviceResourceName

		asID, err := getUint32Param(action+ResourceAccessSpecID, 0, ResourceAccessSpecID)
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
		if err := json.Unmarshal(reqData, llrpReq); err != nil {
			return err
		}
	}

	// SendFor will handle turning ErrorMessages and failing LLRPStatuses into errors.
	if err := c.SendFor(ctx, llrpReq, llrpResp); err != nil {
		return err
	}

	go func(resName, devName string, resp llrp.Incoming) {
		respData, err := json.Marshal(resp)
		if err != nil {
			d.lc.Error("failed to marshal response to %q: %v", resName, err)
			return
		}

		cv := dsModels.NewStringValue(resName, time.Now().UnixNano(), string(respData))
		d.asyncCh <- &dsModels.AsyncValues{
			DeviceName:    devName,
			CommandValues: []*dsModels.CommandValue{cv},
		}
	}(reqs[0].DeviceResourceName, c.Name, llrpResp)

	return nil
}

// Stop the protocol-specific DS code to shutdown gracefully, or
// if the force parameter is 'true', immediately. The driver is responsible
// for closing any in-use channels, including the channel used to send async
// readings (if supported).
func (d *Driver) Stop(force bool) error {
	// Then Logging Client might not be initialized
	if d.lc != nil {
		d.lc.Debug("LLRP-Driver.Stop called", "force", force)
	}

	d.clientsMapMu.Lock()
	defer d.clientsMapMu.Unlock()

	var wg *sync.WaitGroup
	if !force {
		wg = new(sync.WaitGroup)
		wg.Add(len(d.clients))
		defer wg.Wait()
	}

	for _, c := range d.clients {
		go func(c *llrp.Client) {
			d.stopClient(c, force)
			if !force {
				wg.Done()
			}
		}(c)
	}

	d.clients = make(map[string]*llrp.Client)
	return nil
}

// AddDevice is a callback function that is invoked
// when a new Device associated with this Device Service is added
func (d *Driver) AddDevice(deviceName string, protocols protocolMap, adminState contract.AdminState) error {
	d.lc.Debug(fmt.Sprintf("Adding new device: %s protocols: %v adminState: %v",
		deviceName, protocols, adminState))
	_, err := d.getClient(deviceName, protocols)
	return err
}

// UpdateDevice is a callback function that is invoked
// when a Device associated with this Device Service is updated
func (d *Driver) UpdateDevice(deviceName string, protocols protocolMap, adminState contract.AdminState) error {
	d.lc.Debug(fmt.Sprintf("Updating device: %s protocols: %v adminState: %v",
		deviceName, protocols, adminState))
	return nil
}

// RemoveDevice is a callback function that is invoked
// when a Device associated with this Device Service is removed
func (d *Driver) RemoveDevice(deviceName string, p protocolMap) error {
	d.lc.Debug(fmt.Sprintf("Removing device: %s protocols: %v", deviceName, p))
	d.removeClient(deviceName, false)
	return nil
}

// handleAsyncMessages forwards JSON-marshaled messages to EdgeX.
//
// Note that the message types that end up here depend on the subscriptions
// when the Client is created, so if you want to add another,
// you'll need to wire up the handler in the getClient code.
func (d *Driver) handleAsyncMessages(c *llrp.Client, msg llrp.Message) {
	var resourceName string
	var event encoding.BinaryUnmarshaler

	switch msg.Type() {
	default:
		return
	case llrp.MsgReaderEventNotification:
		resourceName = ResourceReaderNotification
		event = &llrp.ReaderEventNotification{}
	case llrp.MsgROAccessReport:
		resourceName = ResourceROAccessReport
		event = &llrp.ROAccessReport{}
	}

	if err := msg.UnmarshalTo(event); err != nil {
		d.lc.Error(err.Error())
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		d.lc.Error(err.Error())
		return
	}

	cv := dsModels.NewStringValue(resourceName, time.Now().UnixNano(), string(data))

	d.asyncCh <- &dsModels.AsyncValues{
		DeviceName:    c.Name,
		CommandValues: []*dsModels.CommandValue{cv},
	}
}

// getOrCreate returns a Client, creating one if needed.
//
// If a Client with this name already exists, it returns it.
// Otherwise, calls the createNew function to get a new Client,
// which it adds to the map and then returns.
func (d *Driver) getClient(name string, p protocolMap) (*llrp.Client, error) {
	// Try with just a read lock.
	d.clientsMapMu.RLock()
	c, ok := d.clients[name]
	d.clientsMapMu.RUnlock()
	if ok {
		return c, nil
	}

	// Probably need other info too, like the device.
	addr, err := getAddr(p)
	if err != nil {
		return nil, err
	}

	// It's important it holds the lock while creating a new Client.
	d.clientsMapMu.Lock()
	defer d.clientsMapMu.Unlock()

	// Check if something else created the Client before we got the lock.
	c, ok = d.clients[name]
	if ok {
		return c, nil
	}

	conn, err := net.DialTimeout(addr.Network(), addr.String(), time.Second*30)
	if err != nil {
		return nil, err
	}

	toEdgex := llrp.MessageHandlerFunc(d.handleAsyncMessages)

	c, err = llrp.NewClient(
		llrp.WithConn(conn), llrp.WithName(name),
		llrp.WithMessageHandler(llrp.MsgROAccessReport, toEdgex),
		llrp.WithMessageHandler(llrp.MsgReaderEventNotification, toEdgex),
	)

	if err != nil {
		return nil, err
	}

	go func() {
		// blocks until the Client is closed
		err = c.Connect()

		if err == nil || errors.Is(err, llrp.ErrClientClosed) {
			return
		}

		d.lc.Error(err.Error())
		// todo: retry the connection?
	}()

	d.clients[name] = c
	return c, nil
}

// removeClient deletes a Client from the clients map.
func (d *Driver) removeClient(deviceName string, force bool) {
	d.clientsMapMu.Lock()
	if c, ok := d.clients[deviceName]; ok {
		delete(d.clients, deviceName)
		go d.stopClient(c, force)
	}
	d.clientsMapMu.Unlock()
}

func (d *Driver) stopClient(c *llrp.Client, force bool) {
	if d.lc != nil {
		d.lc.Info("stopping", "client", c.Name)
	}

	if !force {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := c.Shutdown(ctx); err == nil || errors.Is(err, llrp.ErrClientClosed) {
			return
		} else if d.lc != nil {
			d.lc.Error("error attempting to shutdown client", "error", err.Error())
		}
	}

	if err := c.Close(); err != nil && !errors.Is(err, llrp.ErrClientClosed) {
		d.lc.Error("error attempting to shutdown client", "error", err.Error())
	}
}

// getAddr extracts an address from a protocol mapping.
//
// It expects the map to have {"tcp": {"host": "<ip>", "port": "<port>"}}.
func getAddr(protocols protocolMap) (net.Addr, error) {
	tcpInfo := protocols["tcp"]
	if tcpInfo == nil {
		return nil, errors.New("missing tcp protocol")
	}

	host := tcpInfo["host"]
	port := tcpInfo["port"]
	if host == "" || port == "" {
		return nil, errors.Errorf("tcp missing host or port (%q, %q)", host, port)
	}

	addr, err := net.ResolveTCPAddr("tcp", host+":"+port)
	return addr, errors.Wrapf(err,
		"unable to create addr for tcp protocol (%q, %q)", host, port)
}

func (d *Driver) addProvisionWatcher() error {
	var provisionWatcher contract.ProvisionWatcher
	data, err := ioutil.ReadFile("res/provisionwatcher.json")
	if err != nil {
		d.lc.Error(err.Error())
		return err
	}

	err = provisionWatcher.UnmarshalJSON(data)
	if err != nil {
		d.lc.Error(err.Error())
		return err
	}

	if err := d.service().AddOrUpdateProvisionWatcher(provisionWatcher); err != nil {
		d.lc.Info(err.Error())
		return err
	}

	return nil
}

func (d *Driver) Discover() {
	d.lc.Info("*** Discover was called ***")
	d.deviceCh <- autoDiscover()
	d.lc.Info("scanning complete")
}
