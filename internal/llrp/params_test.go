//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bytes"
	"encoding/binary"
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

	mr.pStack = field{parameter: parameter{length: uint16(len(data))}}

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

func TestMsgReader_set(t *testing.T) {
	data := []byte{
		0x12,
		0x34, 0x56,
		0x78, 0x9a, 0xbc, 0xde,
		0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0x12, 0x34,
	}
	reader := bytes.NewReader(data)
	mr := newMsgReader(message{payload: reader})
	mr.pStack = field{parameter: parameter{length: uint16(len(data))}}

	type T struct {
		U8  uint8
		U16 uint16
		U32 uint32
		U64 uint64
	}

	exp := T{0x12, 0x3456, 0x789abcde, 0xf0123456789a1234}

	v := T{}
	if err := mr.setFields(&v.U8, &v.U16, &v.U32, &v.U64); err != nil {
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
		0x00, 0x02, 0x4b, 0xd4, 0xc0, 0x03, 0x1a, 0x00, // June 25, 1990, 11:18AM EST
	}

	eventData := append(pTimestamp, pConnAttempt...)
	pReaderEvent := append([]byte{
		0x0, 246, // ReaderEventNotificationParameter
		0x0, 0x0, // length (set below)
	}, eventData...)
	binary.BigEndian.PutUint16(pReaderEvent[2:4], uint16(4+len(eventData)))

	reader := bytes.NewReader(pReaderEvent)
	mr := newMsgReader(message{payload: reader})

	ren := readerEventNotification{}
	if err := mr.setFields(&ren); err != nil {
		t.Errorf("%+v", err)
	}

	expTs := timestamp{
		microseconds: uint64(646327080000000),
		isUTC:        true,
	}

	nd := ren.NotificationData
	if nd.TS != expTs {
		t.Errorf("utc timestamp mismatch: %+v != %+v", nd.TS, expTs)
	}
	if nd.ConnectionAttempt == nil || *nd.ConnectionAttempt != anotherConnAttempted {
		t.Errorf("expected ConnectionAttempt to be %v, but it's %v",
			anotherConnAttempted, nd.ConnectionAttempt)
	}
}

func BenchmarkMsgReader_decodeExactly(b *testing.B) {
	pConnAttempt := []byte{
		0x1, 0x0, // ConnectionAttemptEvent
		0x0, 0x6, // size; 4 byte header + 2 byte field
		0x0, 0x4, // 4 == anotherConnAttempted
	}

	pTimestamp := []byte{
		0x0, 128, // UTCTimestamp
		0x0, 12, // header + 8 bytes of microseconds
		0x00, 0x02, 0x4b, 0xd4, 0xc0, 0x03, 0x1a, 0x00, // June 25, 1990, 11:18AM EST
	}

	eventData := append(pTimestamp, pConnAttempt...)
	pReaderEvent := append([]byte{
		0x0, 246, // ReaderEventNotificationParameter
		0x0, 0x0, // length to be set
	}, eventData...)
	binary.BigEndian.PutUint16(pReaderEvent[2:4], uint16(4+len(eventData)))

	reader := bytes.NewReader(pReaderEvent)
	mr := newMsgReader(message{payload: reader})

	ren := readerEventNotification{}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := mr.setFields(&ren); err != nil {
			b.Errorf("%+v", err)
		}

		nd := ren.NotificationData
		if nd.ConnectionAttempt == nil || *nd.ConnectionAttempt != anotherConnAttempted {
			b.Errorf("expected ConnectionAttempt to be %v, but it's %v",
				anotherConnAttempted, *nd.ConnectionAttempt)
		}

		reader.Reset(pReaderEvent)
		mr.pStack = field{}
	}
}
