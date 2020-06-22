//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"encoding/json"
	"io/ioutil"
	"strings"
	"testing"
)

func TestReader_readJSON(t *testing.T) {
	files, err := ioutil.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}

		msg := f.Name()[:len(f.Name())-len(".json")]
		t.Run("TestReadJSON"+msg, readJSON(msg))
	}
}

func readJSON(msgName string) func(t *testing.T) {
	var v interface{}
	switch msgName {
	case GetReaderConfigResponse.String():
		v = &getReaderConfigResponse{}
	case CloseConnectionResponse.String():
		v = &closeConnectionResponse{}
	}

	return func(t *testing.T) {
		if v == nil {
			t.Fatalf("unknown message type: %s", msgName)
		}
		data, err := ioutil.ReadFile("testdata/" + msgName + ".json")

		if err != nil {
			t.Fatal(err)
		}

		if err := json.Unmarshal(data, v); err != nil {
			t.Fatal(err)
		}

		t.Logf("%+v", v)
	}
}
