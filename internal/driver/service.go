package driver

import (
	"fmt"
	"github.com/edgexfoundry/device-sdk-go/pkg/service"
	contract "github.com/edgexfoundry/go-mod-core-contracts/models"
)

type ServiceWrapper interface {
	// Direct pass-through
	Devices() []contract.Device

	// Custom functionality or macros
	AddOrUpdateProvisionWatcher(watcher contract.ProvisionWatcher) error
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

func (s *DeviceSdkService) AddOrUpdateProvisionWatcher(watcher contract.ProvisionWatcher) error {
	existing, err := s.svc.GetProvisionWatcherByName(watcher.Name)

	if err != nil {
		driver.lc.Info(fmt.Sprintf("Adding provision watcher: %s", watcher.Name))
		_, err = s.svc.AddProvisionWatcher(watcher)
	} else {
		driver.lc.Info(fmt.Sprintf("Updating provision watcher: %s", existing.Name))
		err = s.svc.UpdateProvisionWatcher(existing)
	}

	return err
}
