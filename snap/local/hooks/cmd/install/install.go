// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2021 Canonical Ltd
 *
 *  Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except
 *  in compliance with the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License
 * is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express
 * or implied. See the License for the specific language governing permissions and limitations under
 * the License.
 *
 * SPDX-License-Identifier: Apache-2.0'
 */

package main

import (
	"fmt"
	"os"
	"path/filepath"

	hooks "github.com/canonical/edgex-snap-hooks/v2"
)

var cli *hooks.CtlCli = hooks.NewSnapCtl()
var LLRP_RES = "/config/device-rfid-llrp/res"

func installFile(path string) error {
	destFile := hooks.SnapData + LLRP_RES + path
	srcFile := hooks.Snap + LLRP_RES + path

	hooks.Info(fmt.Sprintf("Copying %s to %s\n", srcFile, destFile))

	err := os.MkdirAll(filepath.Dir(destFile), 0755)
	if err != nil {
		return err
	}
	err = hooks.CopyFile(srcFile, destFile)

	return err

}

// installProfiles copies the profile configuration.toml files from $SNAP to $SNAP_DATA.
func installConfig() error {
	return installFile("/configuration.toml")
}

func installProvisionWatchers() error {

	profs := [...]string{"impinj", "llrp"}

	for _, v := range profs {
		path := fmt.Sprintf("/provision_watchers/%s.provision.watcher.json", v)
		err := installFile(path)
		if err != nil {
			return err
		}
	}
	return nil
}

func installDevices() error {
	//No device files
	hooks.Info(fmt.Sprintf("Creating " + hooks.SnapData + LLRP_RES + "/devices"))
	return os.MkdirAll(hooks.SnapData+LLRP_RES+"/devices", 0755)
}

func installDevProfiles() error {

	profs := [...]string{"device", "impinj"}
	for _, v := range profs {
		path := fmt.Sprintf("/profiles/llrp.%s.profile.yaml", v)
		err := installFile(path)
		if err != nil {
			return err
		}
	}
	return nil
}

func main() {
	var err error

	if err = hooks.Init(false, "edgex-device-rfid-llrp"); err != nil {
		fmt.Printf("edgex-device-rfid-llrp::install: initialization failure: %v\n", err)
		os.Exit(1)
	}

	err = installConfig()
	if err != nil {
		hooks.Error(fmt.Sprintf("edgex-device-rfid-llrp:install: %v", err))
		os.Exit(1)
	}

	err = installDevices()
	if err != nil {
		hooks.Error(fmt.Sprintf("edgex-device-rfid-llrp:install: %v", err))
		os.Exit(1)
	}

	err = installDevProfiles()
	if err != nil {
		hooks.Error(fmt.Sprintf("edgex-device-rfid-llrp:install: %v", err))
		os.Exit(1)
	}

	err = installProvisionWatchers()
	if err != nil {
		hooks.Error(fmt.Sprintf("edgex-device-rfid-llrp:install: %v", err))
		os.Exit(1)
	}
}
