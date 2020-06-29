package driver

import (
	dsModels "github.com/edgexfoundry/device-sdk-go/pkg/models"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
	"sync/atomic"
)

type MockSdkService struct {
	devices map[string]contract.Device
	added   uint32
}

func NewMockSdkService() *MockSdkService {
	return &MockSdkService{
		devices: make(map[string]contract.Device),
	}
}

func (s *MockSdkService) clearDevices() {
	s.devices = make(map[string]contract.Device)
	s.resetAddedCount()
}

func (s *MockSdkService) resetAddedCount() {
	atomic.StoreUint32(&s.added, 0)
}

func (s *MockSdkService) Devices() []contract.Device {
	devices := make([]contract.Device, 0, len(s.devices))
	for _, v := range s.devices {
		devices = append(devices, v)
	}
	return devices
}

func (s *MockSdkService) AddDiscoveredDevices(discovered []dsModels.DiscoveredDevice) {
	for _, d := range discovered {
		device := contract.Device{
			Name:            d.Name,
			Protocols:       d.Protocols,
		}

		s.devices[device.Name] = device
		if device.Id == "" {
			device.Id = device.Name
		}
		atomic.AddUint32(&s.added, 1)
	}
}

func (s *MockSdkService) AddDevice(device contract.Device) (id string, err error) {
	s.devices[device.Name] = device
	if device.Id == "" {
		device.Id = device.Name
	}
	atomic.AddUint32(&s.added, 1)
	return device.Id, nil
}

func (s *MockSdkService) AddOrUpdateProvisionWatcher(watcher contract.ProvisionWatcher) error {
	// todo: implement mock
	return nil
}
