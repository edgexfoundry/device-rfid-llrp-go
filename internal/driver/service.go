//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"fmt"
	"github.com/edgexfoundry/device-sdk-go/pkg/service"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
)

type ServiceWrapper interface {
	// Inherit
	Devices() []contract.Device
	GetDeviceByName(name string) (contract.Device, error)
	UpdateDevice(device contract.Device) error
	UpdateDeviceOperatingState(deviceName string, state string) error

	// Pass-through
	DriverConfigs() map[string]string

	// Custom functionality or macros
	AddOrUpdateProvisionWatcher(watcher contract.ProvisionWatcher) error
}

type DeviceSDKService struct {
	*service.Service
}

func (s *DeviceSDKService) AddOrUpdateProvisionWatcher(watcher contract.ProvisionWatcher) error {
	existing, err := s.GetProvisionWatcherByName(watcher.Name)

	if err != nil {
		driver.lc.Info(fmt.Sprintf("Adding provision watcher: %s", watcher.Name))
		_, err = s.AddProvisionWatcher(watcher)
	} else {
		driver.lc.Info(fmt.Sprintf("Updating provision watcher: %s", existing.Name))
		err = s.UpdateProvisionWatcher(existing)
	}

	return err
}

// DriverConfigs retrieves the driver specific configuration
func (s *DeviceSDKService) DriverConfigs() map[string]string {
	return service.DriverConfigs()
}
