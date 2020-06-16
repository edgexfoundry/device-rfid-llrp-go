//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bytes"
	"encoding/binary"
	"io"
	"reflect"
	"testing"
	"time"
)

func TestMsgReader_readUints(t *testing.T) {
	data := []byte{
		0x12,
		0x34, 0x56,
		0x78, 0x9a, 0xbc, 0xde,
		0xf0, 0x12, 0x34, 0x56, 0x78, 0x9a, 0x12, 0x34,
	}
	reader := bytes.NewReader(data)
	mr := NewMsgReader(Message{payload: reader})
	mr.cur = parameter{length: uint16(len(data))}

	type T struct {
		U8  uint8
		U16 uint16
		U32 uint32
		U64 uint64
	}

	exp := T{0x12, 0x3456, 0x789abcde, 0xf0123456789a1234}

	v := T{}
	if err := mr.ReadFields(&v.U8, &v.U16, &v.U32, &v.U64); err != nil {
		t.Error(err)
	}

	if exp != v {
		t.Errorf("expected %+v; got %+v", exp, v)
	}
}

func TestMsgReader_readerEventNotification(t *testing.T) {
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
	binary.BigEndian.PutUint16(pReaderEvent[2:4], uint16(len(pReaderEvent)))

	mr := NewMsgReader(Message{payload: bytes.NewReader(pReaderEvent)})

	ren := readerEventNotification{}
	if err := mr.readParameter(&ren.NotificationData); err != nil {
		t.Errorf("%+v", err)
	}

	expTs := timestamp{microseconds: uint64(646327080000000)}

	nd := ren.NotificationData
	if nd.TS != expTs {
		t.Errorf("utc timestamp mismatch: %+v != %+v", nd.TS, expTs)
	}
	if nd.ConnectionAttempt != ConnAttemptedAgain {
		t.Errorf("expected ConnectionAttempt to be %v, but it's %v",
			ConnAttemptedAgain, nd.ConnectionAttempt)
	}
}

func newReaderEventNotification(ts time.Time, ca ConnectionStatus) readerEventNotification {
	return readerEventNotification{
		NotificationData: readerEventNotificationData{
			TS:                (*timestamp)(nil).fromGoTime(ts),
			ConnectionAttempt: ca,
		},
	}
}

func TestMsgReader_buildNotification(t *testing.T) {
	ts := time.Unix(646327080, 0)
	ren := newReaderEventNotification(ts, ConnFailedReasonUnknown)

	mb := NewMsgBuilder()
	if err := mb.write(&ren); err != nil {
		t.Fatal(err)
	}

	m, err := mb.Finish(ReaderEventNotification)
	if err != nil {
		t.Fatal(err)
	}

	if ReaderEventNotification != m.typ {
		t.Errorf("expected %v, got %v", ReaderEventNotification, m.typ)
	}

	b := make([]byte, m.payloadLen)
	if _, err := io.ReadFull(m.payload, b); err != nil {
		t.Fatal(err)
	}

	exp := []byte{
		0x0, 246, // ReaderEventNotificationParameter
		0x0, 22, // length

		0x0, 128, // UTCTimestamp
		0x0, 12, // header + 8 bytes of microseconds
		0x00, 0x02, 0x4b, 0xd4, 0xc0, 0x03, 0x1a, 0x00, // June 25, 1990, 11:18AM EST

		0x1, 0x0, // ConnectionAttemptEvent
		0x0, 0x6, // size; 4 byte header + 2 byte field
		0x0, 0x3, // failedReasonUnknown
	}

	if !bytes.Equal(exp, b) {
		t.Errorf("\nwant %# 02x\n got %# 02x", exp, b)
	}
}

func TestMsgReader_roundTrip(t *testing.T) {
	ts := time.Unix(646327080, 0)
	out := newReaderEventNotification(ts, ConnFailedReasonUnknown)

	mb := NewMsgBuilder()
	if err := mb.write(&out); err != nil {
		t.Fatal(err)
	}

	m, err := mb.Finish(ReaderEventNotification)
	if err != nil {
		t.Fatal(err)
	}

	mr := NewMsgReader(m)
	in := readerEventNotification{}
	if err := mr.readParameter(&in.NotificationData); err != nil {
		t.Errorf("%+v", err)
	}

	if out != in {
		t.Errorf("mismatch: %+v != %+v", out, in)
	}
}

func TestMsgReader_llrpStatus(t *testing.T) {
	exp := LLRPStatus{
		Code:           StatusMsgParamError,
		ErrDescription: "your parameter offends my sensibilities",
		ParamErr: &ParamError{
			ParamType: ParamCustomParameter,
			ErrorCode: StatusParamParamError,
			ParamError: &ParamError{
				ParamType: ParamAntennaEvent,
				ErrorCode: StatusParamFieldError,
				FieldError: &FieldError{
					FieldNum:  0,
					ErrorCode: StatusFieldInvalid,
				},
				ParamError: &ParamError{
					ParamType: 951,
					ErrorCode: StatusParamParamUnknown,
				},
			},
		},
	}

	mb := NewMsgBuilder()
	if err := mb.writeParam(&exp); err != nil {
		t.Fatal(err)
	}

	m, err := mb.Finish(ErrorMessage)
	if err != nil {
		t.Fatal(err)
	}

	mr := NewMsgReader(m)

	ls := LLRPStatus{}
	if err := mr.readParameter(&ls); err != nil {
		t.Errorf("%+v", err)
	}

	if !reflect.DeepEqual(exp, ls) {
		t.Errorf("expected %v; got %v", exp, ls)
	}

	e := ls.Err()
	if e == nil {
		t.Fatal("unable to get error from LLRPStatus")
	}

	t.Log(e)
}

func BenchmarkMsgReader_readerEventNotification(b *testing.B) {
	ts := time.Unix(646327080, 0)
	out := newReaderEventNotification(ts, ConnFailedReasonUnknown)
	in := readerEventNotification{}

	mb := NewMsgBuilder()
	mr := NewMsgReader(Message{})

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mb.Reset()
		if err := mb.write(&out); err != nil {
			b.Fatal(err)
		}

		m, err := mb.Finish(ReaderEventNotification)
		if err != nil {
			b.Fatal(err)
		}

		mr.Reset(m)
		if err := mr.ReadFields(&in); err != nil {
			b.Errorf("%+v", err)
		}

		if out != in {
			b.Errorf("mismatch: %+v != %+v", out, in)
		}
	}
}
