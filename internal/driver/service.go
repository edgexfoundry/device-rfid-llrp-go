//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"fmt"
	"github.com/edgexfoundry/device-sdk-go/pkg/service"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/logger"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
)

// ServiceWrapper wraps an EdgeX SDK service so it can be easily mocked in tests.
type ServiceWrapper interface {
	// Direct pass-through methods.
	Devices() []contract.Device
	UpdateDeviceOperatingState(name string, state string) error
	GetDeviceByName(name string) (contract.Device, error)

	// Custom functionality or macros.
	AddOrUpdateProvisionWatcher(watcher contract.ProvisionWatcher) error
}

// DeviceSdkService just embeds the normal EdgeX service struct
// to satisfy the ServiceWrapper interface.
type DeviceSdkService struct {
	*service.Service
	lc logger.LoggingClient
}

func (s *DeviceSdkService) AddOrUpdateProvisionWatcher(watcher contract.ProvisionWatcher) error {
	existing, err := s.GetProvisionWatcherByName(watcher.Name)

	if err != nil {
		s.lc.Info(fmt.Sprintf("Adding provision watcher: %s", watcher.Name))
		_, err = s.AddProvisionWatcher(watcher)
	} else {
		s.lc.Info(fmt.Sprintf("Updating provision watcher: %s", existing.Name))
		err = s.UpdateProvisionWatcher(existing)
	}

	return err
}
