package driver

import (
	"fmt"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
)

type MockSdkService struct {
	devices map[string]contract.Device
}

func NewMockSdkService() *MockSdkService {
	return &MockSdkService{
		devices: make(map[string]contract.Device),
	}
}

func (s *MockSdkService) clearDevices() {
	s.devices = make(map[string]contract.Device)
}

func (s *MockSdkService) Devices() []contract.Device {
	devices := make([]contract.Device, 0, len(s.devices))
	for _, v := range s.devices {
		devices = append(devices, v)
	}
	return devices
}

func (s *MockSdkService) GetDeviceByName(name string) (contract.Device, error) {
	if d, found := s.devices[name]; found {
		return d, nil
	} else {
		return contract.Device{}, fmt.Errorf("device not found")
	}
}

func (s *MockSdkService) AddDevice(device contract.Device) (id string, err error) {
	s.devices[device.Name] = device
	if device.Id == "" {
		device.Id = device.Name
	}
	return device.Id, nil
}
