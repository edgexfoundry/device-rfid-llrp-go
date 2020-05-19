//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/edgexfoundry/device-sdk-go/pkg/startup"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/driver"
)

const (
	serviceName string = "edgex-device-llrp"
)

func main() {
	sd := driver.NewProtocolDriver()
	startup.Bootstrap(serviceName, device_llrp.Version, sd)
}
