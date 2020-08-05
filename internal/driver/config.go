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

// configuration holds the values for the driver configuration
type driverConfiguration struct {
	// DiscoverySubnets holds a list of CIDR subnets to scan for devices
	DiscoverySubnets []string
	// ProbeAsyncLimit specifies the maximum simultaneous network probes when performing a discovery
	ProbeAsyncLimit int
	// ProbeTimeoutSeconds specifies the maximum amount of seconds to wait for each IP probe before timing out
	ProbeTimeoutSeconds int
	// ScanPort is the port to scan for LLRP devices on
	ScanPort string
}

// CreateDriverConfig loads driver config from config map
func CreateDriverConfig(configMap map[string]string) (*driverConfiguration, error) {
	config := new(driverConfiguration)
	err := load(configMap, config)
	return config, err
}

// load by reflect to check map key and then fetch the value
func load(configMap map[string]string, config *driverConfiguration) error {
	configValue := reflect.ValueOf(config).Elem()
	for i := 0; i < configValue.NumField(); i++ {
		typeField := configValue.Type().Field(i)
		valueField := configValue.Field(i)

		val, ok := configMap[typeField.Name]
		if !ok {
			return fmt.Errorf("config is missing property '%s'", typeField.Name)
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
		default:
			return fmt.Errorf("config uses unsupported property kind %v for field %v", valueField.Kind(), typeField.Name)
		}
	}
	return nil
}
