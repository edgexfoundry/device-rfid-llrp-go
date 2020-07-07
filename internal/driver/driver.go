//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
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

// HandleReadCommands triggers a protocol Read operation for the specified device.
func (d *Driver) HandleReadCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest) ([]*dsModels.CommandValue, error) {
	d.lc.Debug(fmt.Sprintf("LLRP-Driver.HandleWriteCommands: "+
		"device: %s protocols: %v reqs: %+v", devName, p, reqs))

	if len(reqs) == 0 {
		return nil, errors.New("missing requests")
	}

	_, err := d.getClient(devName, p)
	if err != nil {
		return nil, err
	}

	var responses = make([]*dsModels.CommandValue, len(reqs))

	return responses, nil
}

// HandleWriteCommands passes a slice of CommandRequest struct each representing
// a ResourceOperation for a specific device resource.
// Since the commands are actuation commands, params provide parameters for the individual
// command.
func (d *Driver) HandleWriteCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest, params []*dsModels.CommandValue) error {
	d.lc.Debug(fmt.Sprintf("LLRP-Driver.HandleWriteCommands: "+
		"device: %s protocols: %v reqs: %+v", devName, p, reqs))
	return nil
}

// Stop the protocol-specific DS code to shutdown gracefully, or
// if the force parameter is 'true', immediately. The driver is responsible
// for closing any in-use channels, including the channel used to send async
// readings (if supported).
func (d *Driver) Stop(force bool) error {
	// Then Logging Client might not be initialized
	if d.lc != nil {
		d.lc.Debug(fmt.Sprintf("LLRP-Driver.Stop called: force=%v", force))
	}
	d.clientsMapMu.Lock()
	defer d.clientsMapMu.Unlock()
	for _, r := range d.clients {
		go r.Close() // best effort
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
	d.removeClient(deviceName)
	return nil
}

// getOrCreate returns a Client, creating one if needed.
//
// If a Client with this name already exists, it returns it.
// Otherwise, calls the createNew function to get a new Client,
// which it adds to the map and then returns.
func (d *Driver) getClient(name string, p protocolMap) (*llrp.Client, error) {
	// Try with just a read lock.
	d.clientsMapMu.RLock()
	r, ok := d.clients[name]
	d.clientsMapMu.RUnlock()
	if ok {
		return r, nil
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
	r, ok = d.clients[name]
	if ok {
		return r, nil
	}

	conn, err := net.DialTimeout(addr.Network(), addr.String(), time.Second*30)
	if err != nil {
		return nil, err
	}

	r, err = llrp.NewClient(llrp.WithConn(conn))
	if err != nil {
		return nil, err
	}

	go func() {
		// blocks until the Client is closed
		err = r.Connect()

		if err == nil || errors.Is(err, llrp.ErrClientClosed) {
			return
		}

		d.lc.Error(err.Error())
		// todo: retry the connection?
	}()

	d.clients[name] = r
	return r, nil
}

// removeClient deletes a Client from the clients map.
func (d *Driver) removeClient(deviceName string) {
	d.clientsMapMu.Lock()
	r, ok := d.clients[deviceName]
	if ok {
		go r.Close() // best effort
	}
	delete(d.clients, deviceName)
	d.clientsMapMu.Unlock()
	return
}

// getAddr extracts an address from a protocol mapping.
//
// It expects the map to have {"tcp": {"host": "<ip>", "port": "<port>"}}.
// todo: TLS options? retry/timeout options? LLRP version options?
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
