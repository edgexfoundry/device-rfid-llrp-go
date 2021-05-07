//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"fmt"
	"github.com/pkg/errors"
	"strconv"
)

// driverConfiguration holds the values for the driver configuration
type driverConfiguration struct {
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
}

var (
	// defaultConfig holds default values for each configurable item in case
	// they are not present in the configuration
	defaultConfig = map[string]string{
		"DiscoverySubnets":           "",
		"ProbeAsyncLimit":            "5000",
		"ProbeTimeoutSeconds":        "2",
		"ScanPort":                   "5084",
		"MaxDiscoverDurationSeconds": "300",
	}

	// ErrUnexpectedConfigItems is returned when the input configuration map has extra keys
	// and values that are left over after parsing is complete
	ErrUnexpectedConfigItems = errors.New("unexpected config items")
	// ErrParsingConfigValue is returned when we are unable to parse the value for a config key
	ErrParsingConfigValue = errors.New("unable to parse config value for key")
	// ErrMissingRequiredKey is returned when we are unable to parse the value for a config key
	ErrMissingRequiredKey = errors.New("missing required key")
)

// CreateDriverConfig creates a driverConfiguration object from the strings map provided by EdgeX
func CreateDriverConfig(configMap map[string]string) (*driverConfiguration, error) {
	config := new(driverConfiguration)
	err := config.load(configMap)
	return config, err
}

// load is the internal function that takes the incoming strings map and parses it to fill
// in the values of the driverConfiguration pointer.
// It returns an error wrapping ErrUnexpectedConfigItems if there are additional unused keys and
// values after parsing is complete. This error can be safely ignored using:
// `!errors.Is(err, ErrUnexpectedConfigItems)`
// It may also return an error wrapping ErrParsingConfigValue or ErrMissingRequiredKey
func (config *driverConfiguration) load(configMap map[string]string) error {
	cloneMap := make(map[string]string, len(configMap))
	for k, v := range configMap {
		cloneMap[k] = v
	}

	var err error

	config.DiscoverySubnets, err = pop(cloneMap, "DiscoverySubnets")
	if err != nil {
		return wrapParseError(err, "DiscoverySubnets")
	}

	config.ProbeAsyncLimit, err = popInt(cloneMap, "ProbeAsyncLimit")
	if err != nil {
		return wrapParseError(err, "ProbeAsyncLimit")
	}

	config.ProbeTimeoutSeconds, err = popInt(cloneMap, "ProbeTimeoutSeconds")
	if err != nil {
		return wrapParseError(err, "ProbeTimeoutSeconds")
	}

	config.ScanPort, err = pop(cloneMap, "ScanPort")
	if err != nil {
		return wrapParseError(err, "ScanPort")
	}

	config.MaxDiscoverDurationSeconds, err = popInt(cloneMap, "MaxDiscoverDurationSeconds")
	if err != nil {
		return wrapParseError(err, "MaxDiscoverDurationSeconds")
	}

	// in this case there were extra fields that are not in our config map.
	// these could either be outdated config options or typos
	if len(cloneMap) > 0 {
		driver.lc.Warn(fmt.Sprintf("Got unexpected config keys and values: %+v", cloneMap))
		return errors.Wrapf(ErrUnexpectedConfigItems, "config map: %+v", cloneMap)
	}
	return nil
}

// wrapParseError is a utility function to wrap an error parsing specified key
// with ErrParsingConfigValue
func wrapParseError(err error, key string) error {
	return errors.Wrap(err, errors.Wrap(ErrParsingConfigValue, key).Error())
}

// pop retrieves the value stored in `cloneMap` for the specified key if it exists
// and deletes it from the map. If it does not exist, it uses the default value configured
// for that key. It will return an error wrapping `ErrMissingRequiredKey` if the key is
// missing from `cloneMap` and there is no default value specified for that key.
func pop(cloneMap map[string]string, key string) (string, error) {
	val, ok := cloneMap[key]
	if !ok {
		val, ok = defaultConfig[key]
		if !ok {
			return "", errors.Wrap(ErrMissingRequiredKey, key)
		}
		driver.lc.Info(fmt.Sprintf("Config is missing property '%s', value has been set to the default value of '%s'", key, val))
	}
	// delete each handled field to know if there are any un-handled ones left
	delete(cloneMap, key)
	return val, nil
}

// popInt functions the same way as pop, except it will attempt to convert the value to an int
func popInt(cloneMap map[string]string, key string) (int, error) {
	val, err := pop(cloneMap, key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(val)
}
