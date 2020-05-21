//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"net"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestGetTCPAddr(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		for _, m := range []protocolMap{{
			"tcp": {"host": "127.0.0.1", "port": "1234"},
		}} {
			addr, err := getAddr(m)
			if err != nil {
				t.Error(err)
			}

			expected := "127.0.0.1:1234"
			if expected != addr.String() {
				t.Errorf("expected %s; got %s", expected, addr.String())
			}
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		for _, m := range []protocolMap{{
			"tcp": {"host": "127.0.0.1", "port": "86492"},
		}} {
			if _, err := getAddr(m); err == nil {
				t.Error("expected an error, but didn't get one")
			}
		}
	})
}

func TestReader_readHeader(t *testing.T) {
	type testCase struct {
		desc     string
		data     []byte
		expected header
		invalid  bool
	}

	for i, tc := range []testCase{
		{
			desc: "good header",
			data: []byte{
				0b000_010_00, 0b0000_0001, // rsvd, ver, msg type
				0x0, 0x0, 0x0, 0xA, // msg length
				0x0, 0x0, 0x0, 0x1, // msg id
				// no payload
			},
			expected: header{payloadLen: 0, id: 1, typ: 1, version: 2},
		},
		{
			desc:     "reserved bits are set",
			data:     []byte{0b010_001_00, 1, 0, 0, 0, 0xA, 0, 0, 0, 1},
			expected: header{payloadLen: 0, id: 1, typ: 1, version: 1},
		},
		{
			desc:     "huge payload length",
			data:     []byte{8, 1, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1},
			expected: header{payloadLen: 4294967285, id: 1, typ: 1, version: 2},
		},
		{
			desc:    "impossible message size",
			data:    []byte{8, 1, 0, 0, 0, 1, 0, 0, 0, 1},
			invalid: true,
		},
		{desc: "no data", data: []byte{}, invalid: true},
		{desc: "short data", data: []byte{0x1}, invalid: true},
	} {
		tc := tc
		if tc.desc == "" {
			tc.desc = "case " + strconv.Itoa(i)
		}
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			p1, p2 := net.Pipe()
			go func() {
				if _, err := p1.Write(tc.data); err != nil {
					t.Fatal(err)
				}
			}()

			if err := p2.SetDeadline(time.Now().Add(time.Millisecond * 100)); err != nil {
				t.Fatal(err)
			}

			lr, err := NewReader(WithConn(p2))
			if err != nil {
				t.Fatal(err)
			}

			if tc.invalid {
				if _, err := lr.readHeader(); err == nil {
					t.Error("expected error, but didn't get one")
				} else {
					// to make it a little easier to see what we get
					t.Logf("as expected, got an error: %s", err.Error())
				}
				return
			}

			msg, err := lr.readHeader()
			if err != nil {
				t.Error(err)
			} else if !reflect.DeepEqual(tc.expected, msg) {
				t.Errorf("expected %+v; got %+v", tc.expected, msg)
			}
		})
	}
}

func TestReader_newMessage(t *testing.T) {
	ack := newHdrOnlyMsg(KeepAliveAck)
	expMsg := msgOut{typ: KeepAliveAck}
	if expMsg != ack {
		t.Errorf("expected %+v; got %+v", expMsg, ack)
	}

	client, server := net.Pipe()

	v := uint8(1)
	mid := uint32(1)
	go func() {
		lr, err := NewReader(WithConn(client), WithVersion(v))
		if err != nil {
			t.Fatal(err)
		}
		if err := lr.writeHeader(mid, ack.length, ack.typ); err != nil {
			t.Fatal(err)
		}
	}()

	lr, err := NewReader(WithConn(server))
	if err != nil {
		t.Fatal(err)
	}

	hdr, err := lr.readHeader()
	if err != nil {
		t.Fatal(err)
	}

	expHdr := header{
		typ:     KeepAliveAck,
		version: v,
		id:      mid,
	}
	if expHdr != hdr {
		t.Errorf("expected %+v; got %+v", expHdr, hdr)
	}
}
