//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func testConfig() map[string]string {
	// NOTE: If you change this, you MUST update `TestLoad`!
	return map[string]string{
		"DiscoverySubnets":           "127.0.0.1/32,127.0.1.1/32",
		"ProbeAsyncLimit":            "2257",
		"ProbeTimeoutSeconds":        "5",
		"ScanPort":                   "5084",
		"MaxDiscoverDurationSeconds": "100",
	}
}

func TestLoad(t *testing.T) {
	cfg := testConfig()
	var driverCfg driverConfiguration
	if err := driverCfg.load(cfg); err != nil {
		t.Fatalf("got err: %s", err.Error())
	}

	c := driverCfg
	subnets := strings.Split(c.DiscoverySubnets, ",")
	if c.ProbeAsyncLimit != 2257 ||
		c.ProbeTimeoutSeconds != 5 ||
		c.ScanPort != "5084" ||
		c.MaxDiscoverDurationSeconds != 100 ||
		len(subnets) != 2 ||
		subnets[0] != "127.0.0.1/32" ||
		subnets[1] != "127.0.1.1/32" {

		t.Errorf("One of the value fields is incorrect.\nOriginal: %+v\nParsed: %+v", cfg, driverCfg)
	}
}

func TestEmptyConfigDefaults(t *testing.T) {
	var driverCfg driverConfiguration
	if err := driverCfg.load(map[string]string{}); err != nil {
		t.Fatalf("got err: %s", err.Error())
	}

	configValue := reflect.ValueOf(&driverCfg).Elem()
	for i := 0; i < configValue.NumField(); i++ {
		typeField := configValue.Type().Field(i)
		valueField := configValue.Field(i)
		valueStr := fmt.Sprintf("%v", valueField.Interface())
		valueStr = strings.ReplaceAll(valueStr, "[", "")
		valueStr = strings.ReplaceAll(valueStr, "]", "")
		if valueStr != defaultConfig[typeField.Name] {
			t.Errorf("Field %s, expected value %q, but got %q",
				typeField.Name, defaultConfig[typeField.Name], valueStr)
		}
	}
}

func TestMissingFieldDefaults(t *testing.T) {
	tests := []struct {
		key     string
		valueFn func(driverConfiguration) string
	}{
		{
			key: "ProbeTimeoutSeconds",
			valueFn: func(d driverConfiguration) string {
				return strconv.Itoa(d.ProbeTimeoutSeconds)
			},
		},
		{
			key: "ProbeAsyncLimit",
			valueFn: func(d driverConfiguration) string {
				return strconv.Itoa(d.ProbeAsyncLimit)
			},
		},
		{
			key: "MaxDiscoverDurationSeconds",
			valueFn: func(d driverConfiguration) string {
				return strconv.Itoa(d.MaxDiscoverDurationSeconds)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			cfg := testConfig()
			// delete an item from the config to simulate missing keys/values
			delete(cfg, test.key)

			var driverCfg driverConfiguration
			if err := driverCfg.load(cfg); err != nil {
				t.Fatalf("got err: %s", err.Error())
			}

			val := test.valueFn(driverCfg)
			if val != defaultConfig[test.key] {
				t.Errorf("Field %s, expected value %q, but got %q",
					test.key, defaultConfig["ProbeTimeoutSeconds"], val)
			}
		})
	}
}

func TestErrUnexpectedConfigItems(t *testing.T) {
	cfg := map[string]string{
		"foo": "bar",
	}
	var driverCfg driverConfiguration
	if err := driverCfg.load(cfg); !errors.Is(err, ErrUnexpectedConfigItems) {
		t.Fatalf("expected ErrUnexpectedConfigItems, but got: %v", err)
	}
}
