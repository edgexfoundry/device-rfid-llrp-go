//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"encoding/binary"
	"testing"
	"time"
)

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

	ren := readerEventNotification{}
	if err := ren.UnmarshalBinary(pReaderEvent); err != nil {
		t.Fatalf("%+v", err)
	}

	nd := ren.ReaderEventNotificationData
	expTS := utcTimestamp(646327080000000)
	if nd.UTCTimestamp != expTS {
		t.Errorf("utc timestamp mismatch: %+v != %+v", nd.UTCTimestamp, expTS)
	}
	if nd.ConnectionAttemptEvent == nil ||
		ConnectionAttemptEventType(*nd.ConnectionAttemptEvent) != ConnAttemptedAgain {
		t.Errorf("expected ConnAttemptedAgain, but got %+v", nd)
	}
}

func newReaderEventNotification(ts time.Time, ca ConnectionAttemptEventType) readerEventNotification {
	cae := connectionAttemptEvent(ca)
	return readerEventNotification{
		ReaderEventNotificationData: readerEventNotificationData{
			UTCTimestamp:           utcTimestamp(time.Duration(ts.UnixNano()).Microseconds()),
			ConnectionAttemptEvent: &cae,
		},
	}
}

/*
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
	exp := llrpStatus{
		Status:           StatusMsgParamError,
		ErrorDescription: "your parameter offends my sensibilities",
		ParameterError: &parameterError{
			ParameterType: ParamCustom,
			ErrorCode:     StatusParamParamError,
			ParameterError: &parameterError{
				ParameterType: ParamAntennaEvent,
				ErrorCode:     StatusParamFieldError,
				FieldError: &fieldError{
					FieldIndex: 0,
					ErrorCode:  StatusFieldInvalid,
				},
				ParameterError: &parameterError{
					ParameterType: 951,
					ErrorCode:     StatusParamParamUnknown,
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

	ls := llrpStatus{}
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

	t.Logf("This is what an LLRPStatus with an error will look like: %+v", e)
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

		if reflect.DeepEqual(out, in) {
			b.Errorf("mismatch: %+v != %+v", out, in)
		}
	}
}
*/

func BenchmarkReaderEventNotification_UnmarshalBinary(b *testing.B) {
	data := []byte{
		0x0, 246, // ReaderEventNotificationParameter
		0x0, 22, // length

		0x0, 128, // UTCTimestamp
		0x0, 12, // header + 8 bytes of microseconds
		0x00, 0x02, 0x4b, 0xd4, 0xc0, 0x03, 0x1a, 0x00, // June 25, 1990, 11:18AM EST

		0x1, 0x0, // ConnectionAttemptEvent
		0x0, 0x6, // size; 4 byte header + 2 byte field
		0x0, 0x3, // failedReasonUnknown
	}

	b.ReportAllocs()
	b.ResetTimer()
	ren := readerEventNotification{}
	for i := 0; i < b.N; i++ {
		if err := ren.UnmarshalBinary(data); err != nil {
			b.Fatalf("%+v", err)
		}
	}
}

func TestReaderEventNotification_UnmarshalBinary(t *testing.T) {
	data := []byte{
		0x0, 246, // ReaderEventNotificationParameter
		0x0, 22, // length

		0x0, 128, // UTCTimestamp
		0x0, 12, // header + 8 bytes of microseconds
		0x00, 0x02, 0x4b, 0xd4, 0xc0, 0x03, 0x1a, 0x00, // June 25, 1990, 11:18AM EST

		0x1, 0x0, // ConnectionAttemptEvent
		0x0, 0x6, // size; 4 byte header + 2 byte field
		0x0, 0x3, // failedReasonUnknown
	}

	ren := readerEventNotification{}
	if err := ren.UnmarshalBinary(data); err != nil {
		t.Fatalf("%+v", err)
	}

	cae := connectionAttemptEvent(ConnFailedReasonUnknown)
	exp := readerEventNotification{
		ReaderEventNotificationData: readerEventNotificationData{
			UTCTimestamp:           utcTimestamp(0x00024bd4c0031a00),
			ConnectionAttemptEvent: &cae,
		},
	}

	if exp.ReaderEventNotificationData.UTCTimestamp != ren.ReaderEventNotificationData.UTCTimestamp {
		t.Fatalf("expected timestamp %+v; got %+v",
			exp.ReaderEventNotificationData.UTCTimestamp,
			ren.ReaderEventNotificationData.UTCTimestamp)
	}

	if nil == ren.ReaderEventNotificationData.ConnectionAttemptEvent {
		t.Fatalf("expected non-nil timestamp and connection attempt; got %+v", ren.ReaderEventNotificationData.ConnectionAttemptEvent)
	}

	if ConnFailedReasonUnknown != ConnectionAttemptEventType(*ren.ReaderEventNotificationData.ConnectionAttemptEvent) {
		t.Fatalf("expected ConnFailedReasonUnknown; got %+v", ren.ReaderEventNotificationData.ConnectionAttemptEvent)
	}
}
