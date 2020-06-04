//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"fmt"
	"github.com/pkg/errors"
	"net"
	"sync"
	"time"

	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
)

var once sync.Once
var driver *Driver

type Driver struct {
	lc       logger.LoggingClient
	asyncCh  chan<- *dsModels.AsyncValues
	deviceCh chan<- []dsModels.DiscoveredDevice

	readers     map[string]*Reader
	readerMapMu sync.RWMutex
}

func NewProtocolDriver() dsModels.ProtocolDriver {
	once.Do(func() {
		driver = &Driver{
			readers: make(map[string]*Reader),
		}
	})
	return driver
}

// Initialize performs protocol-specific initialization for the device
// service.
func (d *Driver) Initialize(lc logger.LoggingClient, asyncCh chan<- *dsModels.AsyncValues, deviceCh chan<- []dsModels.DiscoveredDevice) error {
	d.lc = lc
	d.asyncCh = asyncCh
	d.deviceCh = deviceCh
	go d.Discover()
	return nil
}

type protocolMap = map[string]contract.ProtocolProperties

// HandleReadCommands triggers a llrpProtocol Read operation for the specified device.
func (d *Driver) HandleReadCommands(devName string, p protocolMap, reqs []dsModels.CommandRequest) ([]*dsModels.CommandValue, error) {
	d.lc.Debug(fmt.Sprintf("LLRP-Driver.HandleWriteCommands: "+
		"device: %s protocols: %v reqs: %+v", devName, p, reqs))

	if len(reqs) == 0 {
		return nil, errors.New("missing requests")
	}

	_, err := d.getReader(devName, p)
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

// Stop the llrpProtocol-specific DS code to shutdown gracefully, or
// if the force parameter is 'true', immediately. The driver is responsible
// for closing any in-use channels, including the channel used to send async
// readings (if supported).
func (d *Driver) Stop(force bool) error {
	// Then Logging Client might not be initialized
	if d.lc != nil {
		d.lc.Debug(fmt.Sprintf("LLRP-Driver.Stop called: force=%v", force))
	}
	d.readerMapMu.Lock()
	defer d.readerMapMu.Unlock()
	for _, r := range d.readers {
		go r.Close() // best effort
	}
	d.readers = make(map[string]*Reader)
	return nil
}

// AddDevice is a callback function that is invoked
// when a new Device associated with this Device Service is added
func (d *Driver) AddDevice(deviceName string, protocols protocolMap, adminState contract.AdminState) error {
	d.lc.Debug(fmt.Sprintf("Adding new device: %s protocols: %v adminState: %v",
		deviceName, protocols, adminState))
	_, err := d.getReader(deviceName, protocols)
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
	d.removeReader(deviceName)
	return nil
}

// getOrCreate returns a Reader, creating one if needed.
//
// If a Reader with this name already exists, it returns it.
// Otherwise, calls the createNew function to get a new Reader,
// which it adds to the map and then returns.
func (d *Driver) getReader(name string, p protocolMap) (*Reader, error) {
	// Try with just a read lock.
	d.readerMapMu.RLock()
	r, ok := d.readers[name]
	d.readerMapMu.RUnlock()
	if ok {
		return r, nil
	}

	// Probably need other info too, like the device.
	addr, err := getAddr(p)
	if err != nil {
		return nil, err
	}

	// It's important it holds the lock while creating a new Reader
	// as multiple concurrent connection attempts can reset the Reader.
	d.readerMapMu.Lock()
	defer d.readerMapMu.Unlock()

	// Check if something else created the Reader before we got the lock.
	r, ok = d.readers[name]
	if ok {
		return r, nil
	}

	// todo: configurable timeouts
	conn, err := net.DialTimeout(addr.Network(), addr.String(), time.Second*30)
	if err != nil {
		return nil, err
	}

	r, err = NewReader(WithConn(conn))
	if err != nil {
		return nil, err
	}

	go func() {
		// blocks until the Reader is closed
		err = r.Connect()

		if err == nil || errors.Is(err, ErrReaderClosed) {
			return
		}

		d.lc.Error(err.Error())
		// todo: retry the connection
	}()

	d.readers[name] = r
	return r, nil
}

// removeReader deletes a Reader from the readers map.
func (d *Driver) removeReader(deviceName string) {
	d.readerMapMu.Lock()
	r, ok := d.readers[deviceName]
	if ok {
		go r.Close() // best effort
	}
	delete(d.readers, deviceName)
	d.readerMapMu.Unlock()
	return
}

// getAddr extracts an address from a llrpProtocol mapping.
//
// It expects the map to have {"tcp": {"host": "<ip>", "port": "<port>"}}.
// todo: TLS options? retry/timeout options? LLRP version options?
func getAddr(protocols protocolMap) (net.Addr, error) {
	tcpInfo := protocols["tcp"]
	if tcpInfo == nil {
		return nil, errors.New("missing tcp llrpProtocol")
	}

	host := tcpInfo["host"]
	port := tcpInfo["port"]
	if host == "" || port == "" {
		return nil, errors.Errorf("tcp missing host or port (%q, %q)", host, port)
	}

	addr, err := net.ResolveTCPAddr("tcp", host+":"+port)
	return addr, errors.Wrapf(err,
		"unable to create addr for tcp llrpProtocol (%q, %q)", host, port)
}

func (d *Driver) Discover() {
	mon, wg, err := RunScanner()
	if err != nil {
		d.lc.Error(err.Error())
	} else {
		mon.Stop()
		wg.Wait()
		d.lc.Info("scanning complete")
	}
}
