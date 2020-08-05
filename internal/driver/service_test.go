//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
	"sync/atomic"
)

type MockSDKService struct {
	devices map[string]contract.Device
	added   uint32
	Config  map[string]string
}

func NewMockSdkService() *MockSDKService {
	return &MockSDKService{
		devices: make(map[string]contract.Device),
	}
}

func (s *MockSDKService) clearDevices() {
	s.devices = make(map[string]contract.Device)
	s.resetAddedCount()
}

func (s *MockSDKService) resetAddedCount() {
	atomic.StoreUint32(&s.added, 0)
}

func (s *MockSDKService) Devices() []contract.Device {
	devices := make([]contract.Device, len(s.devices))
	for _, v := range s.devices {
		devices = append(devices, v)
	}
	return devices
}

func (s *MockSDKService) AddDiscoveredDevices(discovered []dsModels.DiscoveredDevice) {
	for _, d := range discovered {
		_, _ = s.AddDevice(contract.Device{
			Name:      d.Name,
			Protocols: d.Protocols,
			Profile: contract.DeviceProfile{
				Name: LLRPDeviceProfile,
			},
		})
	}
}

func (s *MockSDKService) AddDevice(device contract.Device) (id string, err error) {
	if device.Id == "" {
		device.Id = device.Name
	}
	s.devices[device.Name] = device
	atomic.AddUint32(&s.added, 1)
	return device.Id, nil
}

func (s *MockSDKService) AddOrUpdateProvisionWatcher(watcher contract.ProvisionWatcher) error {
	// todo: implement mock
	return nil
}

func (s *MockSDKService) DriverConfigs() map[string]string {
	return s.Config
}
