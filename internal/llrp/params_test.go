//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestMsgReader_getUint(t *testing.T) {
	data := []byte{
		0x12,
		0x34, 0x56,
		0x78, 0x9a, 0xbc, 0xde,
		0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0x12, 0x34,
	}
	mr := newMsgReader(message{
		payload: bytes.NewReader(data),
		header: header{
			payloadLen: uint32(len(data)),
			id:         0,
			typ:        ReaderEventNotification,
			version:    Version1_0_1,
		},
	})

	for _, test := range []struct {
		sz  int
		exp uint64
	}{
		{sz: 1, exp: 0x12},
		{sz: 2, exp: 0x3456},
		{sz: 4, exp: 0x789abcde},
		{sz: 8, exp: 0xf0123456789a1234},
	} {
		u, err := mr.getUint(test.sz)
		if err != nil {
			t.Error(err)
		}
		if test.exp != u {
			t.Errorf("expected %[2]d (%#0[1]*x); got %d (%#0[1]*[3]x)",
				test.sz*2, test.exp, u)
		}
	}
}

func TestMsgReader_setUint(t *testing.T) {
	data := []byte{
		0x12,
		0x34, 0x56,
		0x78, 0x9a, 0xbc, 0xde,
		0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0x12, 0x34,
	}
	mr := newMsgReader(message{
		payload: bytes.NewReader(data),
		header: header{
			payloadLen: uint32(len(data)),
			id:         0,
			typ:        ReaderEventNotification,
			version:    Version1_0_1,
		},
	})

	type T struct {
		U8  uint8
		U16 uint16
		U32 uint32
		U64 uint64
	}

	exp := T{0x12, 0x3456, 0x789abcde, 0xf0123456789a1234}

	var v T
	rv := reflect.ValueOf(&v).Elem()
	for i, n := range []int{1, 2, 4, 8} {
		rv.Field(i)
		if err := mr.setUintV(rv.Field(i), n); err != nil {
			t.Error(err)
		}
	}

	if exp != v {
		t.Errorf("expected %+v; got %+v", exp, v)
	}
}

func TestMsgReader_set(t *testing.T) {
	data := []byte{
		0x12,
		0x34, 0x56,
		0x78, 0x9a, 0xbc, 0xde,
		0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0x12, 0x34,
	}
	reader := bytes.NewReader(data)
	mr := newMsgReader(message{
		payload: reader,
		header: header{
			payloadLen: uint32(len(data)),
			id:         0,
			typ:        ReaderEventNotification,
			version:    Version1_0_1,
		},
	})

	type T struct {
		U8  uint8
		U16 uint16
		U32 uint32
		U64 uint64
	}

	exp := T{0x12, 0x3456, 0x789abcde, 0xf0123456789a1234}

	v := T{}
	rv := reflect.ValueOf(&v).Elem()
	for i := 0; i < 4; i++ {
		if err := mr.set(rv.Field(i)); err != nil {
			t.Error(err)
		}
	}

	if exp != v {
		t.Errorf("expected %+v; got %+v", exp, v)
	}

	reader.Reset(data)

	v = T{}
	rv = reflect.ValueOf(&v).Elem()
	if err := mr.set(rv); err != nil {
		t.Error(err)
	}

	if exp != v {
		t.Errorf("expected %+v; got %+v", exp, v)
	}
}

func TestMsgReader_decodeExactly(t *testing.T) {
	pConnAttempt := []byte{
		0x1, 0x0, // ConnectionAttemptEvent
		0x0, 0x6, // size; 4 byte header + 2 byte field
		0x0, 0x4, // 4 == anotherConnAttempted
	}

	pTimestamp := []byte{
		0x0, 128, // UTCTimestamp
		0x0, 12, // header + 8 bytes of microseconds
		0x00, 0x02, 0x4b, 0xd4, 0xd0, 0x03, 0x1a, 0x00, // June 25, 1990, 11:18AM EST
	}

	eventData := append(pTimestamp, pConnAttempt...)
	pReaderEvent := append([]byte{
		0x0, 246, // ReaderEventNotificationParameter
		0x0, 0x0, // length to be set
	}, eventData...)
	binary.BigEndian.PutUint16(pReaderEvent[2:4], uint16(4+len(eventData)))

	t.Logf("%# 02x", pReaderEvent)

	reader := bytes.NewReader(pReaderEvent)
	mr := newMsgReader(message{
		payload: reader,
		header: header{
			payloadLen: uint32(len(pReaderEvent)),
			typ:        ReaderEventNotification,
			version:    Version1_0_1,
		},
	})

	ren := readerEventNotificationData{
		TS:                timestamp{UTC: new(utcTimestamp)},
		ConnectionAttempt: new(connAttemptEventParam),
	}
	if err := mr.decode(&ren); err != nil {
		t.Errorf("%+v", err)
	}

	if ren.ConnectionAttempt == nil || anotherConnAttempted != *ren.ConnectionAttempt {
		t.Errorf("expected %+v; got %+v", anotherConnAttempted, ren)
	}

}
