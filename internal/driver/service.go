package driver

import (
	"github.com/edgexfoundry/device-sdk-go/pkg/service"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
)

type ServiceWrapper interface {
	Devices() []contract.Device
	GetDeviceByName(name string) (contract.Device, error)
	AddDevice(device contract.Device) (id string, err error)
}

type DeviceSdkService struct {
	svc *service.Service
}

func RunningService() *DeviceSdkService {
	return &DeviceSdkService{svc: service.RunningService()}
}

func (s *DeviceSdkService) Devices() []contract.Device {
	return s.svc.Devices()
}

func (s *DeviceSdkService) GetDeviceByName(name string) (contract.Device, error) {
	return s.svc.GetDeviceByName(name)
}

func (s *DeviceSdkService) AddDevice(device contract.Device) (id string, err error) {
	return s.svc.AddDevice(device)
}
