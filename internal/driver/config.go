//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// driverConfiguration holds the values for the driver configuration
type driverConfiguration struct {
	// DiscoverySubnets holds a list of CIDR subnets to scan for devices
	DiscoverySubnets []string
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
		"ProbeAsyncLimit":            "1000",
		"ProbeTimeoutSeconds":        "2",
		"ScanPort":                   "5084",
		"MaxDiscoverDurationSeconds": "300",
	}
)

// CreateDriverConfig loads driver config from config map strings from EdgeX
func CreateDriverConfig(configMap map[string]string) (*driverConfiguration, error) {
	config := new(driverConfiguration)
	err := load(configMap, config)
	return config, err
}

// load by reflect to check map key and then fetch the value
func load(configMap map[string]string, config *driverConfiguration) error {
	cfgMap := configMap

	configValue := reflect.ValueOf(config).Elem()
	for i := 0; i < configValue.NumField(); i++ {
		typeField := configValue.Type().Field(i)
		valueField := configValue.Field(i)

		val, ok := cfgMap[typeField.Name]
		if !ok {
			val, ok = defaultConfig[typeField.Name]
			if !ok {
				return fmt.Errorf("config is missing required property '%s'", typeField.Name)
			} else {
				driver.lc.Debug(fmt.Sprintf("Config is missing property '%s', value has been set to the default value of '%s'", typeField.Name, val))
			}
		}
		if !valueField.CanSet() {
			return fmt.Errorf("cannot set field '%s'", typeField.Name)
		}

		switch valueField.Kind() {
		case reflect.Int:
			intVal, err := strconv.Atoi(val)
			if err != nil {
				return err
			}
			valueField.SetInt(int64(intVal))
		case reflect.Bool:
			boolVal, err := strconv.ParseBool(val)
			if err != nil {
				return err
			}
			valueField.SetBool(boolVal)
		case reflect.String:
			valueField.SetString(val)
		case reflect.Slice:
			splitVals := strings.Split(val, ",")
			var slice reflect.Value
			switch typeField.Type.Elem().Kind() {
			case reflect.String:
				slice = reflect.ValueOf(splitVals)
			case reflect.Int:
				slice = reflect.MakeSlice(typeField.Type, len(splitVals), len(splitVals))
				for idx, toConvert := range splitVals {
					intVal, err := strconv.Atoi(toConvert)
					if err != nil {
						return err
					}
					slice.Index(idx).SetInt(int64(intVal))
				}
			default:
				return fmt.Errorf("config uses unsupported slice property kind %v for field %v", typeField.Type.Elem().Kind(), typeField.Name)
			}
			slice = reflect.AppendSlice(valueField, slice)
			valueField.Set(slice)
			// delete each handled field to know if there are any un-handled ones left
			delete(cfgMap, typeField.Name)
		default:
			return fmt.Errorf("config uses unsupported property kind %v for field %v", valueField.Kind(), typeField.Name)
		}
	}
	// in this case there were extra fields that are not in our config map.
	// these could either be outdated config options or typos
	if len(cfgMap) > 0 {
		driver.lc.Warn(fmt.Sprintf("Got unexpected config values: %+v", cfgMap))
	}
	return nil
}
