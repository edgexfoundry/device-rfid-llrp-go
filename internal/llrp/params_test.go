//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import "testing"

func TestParamParser(t *testing.T) {
	errDesc := "error description"
	errLen := uint16(len(errDesc))

	payload := append([]byte{
		0x01, 0x1F, // type 287, LLRPStatus Parameter
		0x00, 0x00, // total length (including 4 byte header)
		0x00, 0x00, // StatusCode
		uint8(errLen >> 8), uint8(errLen & 0xFF), // Error Description ByteCount
	}, []byte(errDesc)...)
	payload[2] = uint8(len(payload) >> 8)
	payload[3] = uint8(len(payload) & 0xFF)

	msg, err := newByteMessage(payload, ErrorMessage)
	if err != nil {
		t.Fatal(err)
	}

	pr := newParamReader(msg)

	pTyp, err := pr.next()
	if err != nil {
		t.Fatal(err)
	}
	if PStatus != pTyp {
		t.Fatalf("expected %v; got %v", PStatus, pTyp)
	}

	t.Log(pr.cur)
	ls, err := pr.llrpStatus(pr.cur.length)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	t.Logf("%+v", ls)
}
