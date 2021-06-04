//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/edgexfoundry/device-rfid-llrp-go"
	"github.com/edgexfoundry/device-rfid-llrp-go/internal/driver"
	"github.com/edgexfoundry/device-sdk-go/pkg/startup"
)

func main() {
	sd := driver.Instance()
	startup.Bootstrap(driver.ServiceName, device_llrp.Version, sd)
}
