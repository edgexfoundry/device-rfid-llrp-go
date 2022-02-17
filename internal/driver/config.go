//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

// CustomConfig holds the values for the driver configuration
type CustomConfig struct {
	// DiscoverySubnets holds a comma separated list of CIDR subnets to scan for devices. This is kept as a string instead
	// of a string slice because when using EdgeX's Consul implementation, the data returned is a slice of 1 element
	// containing the entire string.
	DiscoverySubnets string
	// ProbeAsyncLimit specifies the maximum simultaneous network probes when performing a discovery
	ProbeAsyncLimit int
	// ProbeTimeoutSeconds specifies the maximum amount of seconds to wait for each IP probe before timing out
	ProbeTimeoutSeconds int
	// ScanPort is the port to scan for LLRP devices on
	ScanPort string
	// MaxDiscoverDurationSeconds is the maximum amount of seconds for a discovery to run. It is important
	// to have this configured in the case of larger subnets such as /16 and /8
	MaxDiscoverDurationSeconds int

	// Location of Provision Watchers
	ProvisionWatcherDir string
}

// ServiceConfig a struct that wraps CustomConfig which holds the values for driver configuration
type ServiceConfig struct {
	AppCustom CustomConfig
}

// UpdateFromRaw updates the service's full configuration from raw data received from
// the Service Provider.
func (c *ServiceConfig) UpdateFromRaw(rawConfig interface{}) bool {
	configuration, ok := rawConfig.(*ServiceConfig)
	if !ok {
		return false
	}

	*c = *configuration

	return true
}
