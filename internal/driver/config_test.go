//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUpdateFromRaw(t *testing.T) {
	expectedConfig := &ServiceConfig{
		AppCustom: CustomConfig{
			DiscoverySubnets:           "127.0.0.1/32,127.0.1.1/32",
			ProbeAsyncLimit:            50,
			ProbeTimeoutSeconds:        1,
			ScanPort:                   "666",
			MaxDiscoverDurationSeconds: 5,
		},
	}
	testCases := []struct {
		Name      string
		rawConfig interface{}
		isValid   bool
	}{
		{
			Name:      "valid",
			isValid:   true,
			rawConfig: expectedConfig,
		},
		{
			Name:      "not valid",
			isValid:   false,
			rawConfig: expectedConfig.AppCustom,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.Name, func(t *testing.T) {
			actualConfig := ServiceConfig{}

			ok := actualConfig.UpdateFromRaw(testCase.rawConfig)

			assert.Equal(t, testCase.isValid, ok)
			if testCase.isValid {
				assert.Equal(t, expectedConfig, &actualConfig)
			}
		})
	}

}
