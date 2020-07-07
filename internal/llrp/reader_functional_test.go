//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"context"
	"encoding"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"net"
	"strconv"
	"testing"
	"time"
)

// ex: go test -reader="localhost:5084"
// if using Goland, put that in the 'program arguments' part of the test config
var readerAddr = flag.String("reader", "", "address of an LLRP reader; enables functional tests")
var update = flag.Bool("update", false, "rather than testing, record messages to the testdata directory")

func TestClientFunctional(t *testing.T) {
	addr := *readerAddr
	if addr == "" {
		t.Skip("no reader set for functional tests; use -test.reader=\"host:port\" to run")
	}

	if *update {
		if err := collectData(); err != nil && !errors.Is(err, ErrClientClosed) {
			t.Fatal(err)
		}
		t.Skip("collected data instead of running tests")
		return
	}

	t.Run("hangup", testHangUp)
	t.Run("addROSpec", testAddROSpec)

	for i := 0; i < 1; i++ {
		t.Run("connect "+strconv.Itoa(i), testConnect)
	}
}

// collectData populates the testdata directory for use in future tests.
func collectData() error {
	conn, err := net.Dial("tcp", *readerAddr)
	if err != nil {
		return err
	}

	if err := conn.SetDeadline(time.Now().Add(120 * time.Second)); err != nil {
		return err
	}

	r, err := NewClient(WithConn(conn))
	if err != nil {
		return err
	}
	defer r.Close()

	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- r.Connect()
	}()

	select {
	case <-r.ready:
	case <-time.After(10 * time.Second):
		return errors.New("reader not ready")
	}

	if r.version > Version1_0_1 {
		if err := getAndWrite(r, MsgGetSupportedVersion, nil, &GetSupportedVersionResponse{}); err != nil {
			return err
		}
	}

	var errs []error
	for _, toSend := range []struct {
		mt  MessageType
		out encoding.BinaryMarshaler
		in  encoding.BinaryUnmarshaler
	}{
		{MsgGetReaderConfig, &GetReaderConfig{}, &GetReaderConfigResponse{}},
		{MsgGetReaderCapabilities, &GetReaderCapabilities{}, &GetReaderCapabilitiesResponse{}},
		{MsgGetROSpecs, nil, &GetROSpecsResponse{}},
		{MsgGetAccessSpecs, nil, &GetAccessSpecsResponse{}},
		{MsgGetReport, nil, &ROAccessReport{}},
		{MsgCloseConnection, nil, &CloseConnectionResponse{}},
	} {
		if err := getAndWrite(r, toSend.mt, toSend.out, toSend.in); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				errs = append(errs, errors.WithMessagef(err, "failed to get response for %v", toSend.mt))
			} else {
				return err
			}
		}
	}

	if err := r.Close(); err != nil {
		errs = append(errs, err)
	}

	if err := <-connErrs; err != nil {
		errs = append(errs, err)
	}

	{
		var errMsg string
		for _, e := range errs {
			errMsg += e.Error() + "\n"
		}
		if errMsg != "" {
			return errors.New(errMsg)
		}
	}

	return <-connErrs
}

func getAndWrite(r *Client, mt MessageType, payload encoding.BinaryMarshaler, resultValue encoding.BinaryUnmarshaler) error {
	var data []byte
	if payload != nil {
		var err error
		data, err = payload.MarshalBinary()
		if err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resultT, result, err := r.SendMessage(ctx, mt, data)
	if err != nil {
		return err
	}

	expR, ok := mt.responseType()
	if ok && expR != resultT {
		return errors.Errorf("expected %v; got %v", expR, mt)
	}

	name := resultT.String()[len("Msg"):]
	bfn := fmt.Sprintf("testdata/%v.bytes", name)
	jfn := fmt.Sprintf("testdata/%v.json", name)

	if err := ioutil.WriteFile(bfn, result, 0644); err != nil {
		return err
	}

	if err := resultValue.UnmarshalBinary(result); err != nil {
		return err
	}

	j, err := json.MarshalIndent(resultValue, "", "\t")
	if err != nil {
		return err
	}

	if err := ioutil.WriteFile(jfn, j, 0644); err != nil {
		return err
	}

	return nil
}

func BenchmarkClientFunctional(b *testing.B) {
	addr := *readerAddr
	if addr == "" {
		b.Skip("no reader set for functional tests; use -test.reader=\"host:port\" to run")
	}

	b.Run("Connect", func(bb *testing.B) {
		bb.ReportAllocs()
		for i := 0; i < bb.N; i++ {
			benchmarkConnect(bb)
		}
	})

	b.Run("Send", benchmarkSend)
}

func testHangUp(t *testing.T) {
	conn, err := net.Dial("tcp", *readerAddr)
	if err != nil {
		t.Fatal(err)
	}
	b := [HeaderSz]byte{}
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		t.Error(err)
	}
	conn.Close()
}

func benchmarkSend(b *testing.B) {
	conn, err := net.Dial("tcp", *readerAddr)

	if err != nil {
		b.Fatal(err)
	}

	if err := conn.SetDeadline(time.Now().Add(240 * time.Second)); err != nil {
		b.Fatal(err)
		return
	}

	r, err := NewClient(WithConn(conn), WithVersion(Version1_1), WithLogger(devNullLog{}))
	if err != nil {
		b.Fatal(err)
	}

	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- r.Connect()
	}()

	defer func() {
		_ = r.Close()
		for err := range connErrs {
			if !errors.Is(err, ErrClientClosed) {
				b.Fatalf("%+v", err)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mt, m, err := r.SendMessage(ctx, MsgGetSupportedVersion, nil)
		if MsgGetSupportedVersionResponse != mt {
			b.Errorf("expected %v; got %v", MsgGetSupportedVersionResponse, mt)
		}
		if err != nil {
			b.Fatal(err)
		}
		if len(m) == 0 {
			b.Fatal("no message response")
		}
	}
	b.StopTimer()

	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		b.Errorf("%+v", err)
		if err == context.DeadlineExceeded {
		}
		if err := r.Close(); err != nil {
			b.Error(err)
		}
	}
}

func benchmarkConnect(b *testing.B) {
	conn, err := net.Dial("tcp", *readerAddr)

	if err != nil {
		b.Fatal(err)
	}

	if err := conn.SetDeadline(time.Now().Add(120 * time.Second)); err != nil {
		b.Fatal(err)
		return
	}

	r, err := NewClient(WithConn(conn), WithVersion(Version1_1), WithLogger(devNullLog{}))
	if err != nil {
		b.Fatal(err)
	}

	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- r.Connect()
	}()

	defer func() {
		_ = r.Close()
		for err := range connErrs {
			if !errors.Is(err, ErrClientClosed) {
				b.Fatalf("%+v", err)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = r.SendMessage(ctx, MsgGetSupportedVersion, nil)
	if err != nil {
		b.Fatal(err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		b.Errorf("%+v", err)
		if err == context.DeadlineExceeded {
		}
		if err := r.Close(); err != nil {
			b.Error(err)
		}
	}
}

func testConnect(t *testing.T) {
	conn, err := net.Dial("tcp", *readerAddr)
	if err != nil {
		t.Fatal(err)
	}

	if err := conn.SetDeadline(time.Now().Add(120 * time.Second)); err != nil {
		t.Fatal(err)
		return
	}

	r, err := NewClient(WithConn(conn))
	if err != nil {
		t.Fatal(err)
	}

	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- r.Connect()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	payload := []byte{0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0}
	mt, resp, err := r.SendMessage(ctx, MsgGetReaderConfig, payload)
	cancel()

	if err != nil {
		t.Error(err)
	} else if resp == nil {
		t.Error("expected non-nil response")
	}

	if MsgGetReaderConfigResponse != mt {
		t.Errorf("expected %v; got %v", MsgGetReaderConfigResponse, mt)
		if mt == MsgErrorMessage {
			var errMsg ErrorMessage

			if err := errMsg.UnmarshalBinary(resp); err != nil {
				t.Error(err)
				t.Logf("%# 02x", resp)
			} else if err := errMsg.LLRPStatus.Err(); err != nil {
				t.Logf("%+v", errMsg)
				t.Error(err)
			} else {
				if r, err := json.MarshalIndent(errMsg, "", "\t"); err != nil {
					t.Error(err)
				} else {
					t.Log(string(r))
				}
			}
		}
	} else {
		var conf GetReaderConfigResponse
		if err := conf.UnmarshalBinary(resp); err != nil {
			t.Errorf("%+v", err)
			t.Logf("%# 02x", resp)
		} else if err := conf.LLRPStatus.Err(); err != nil {
			t.Error(err)
		}

		t.Logf("%+v", conf)

		if r, err := json.MarshalIndent(conf, "", "\t"); err != nil {
			t.Error(err)
		} else {
			t.Log(string(r))
		}
	}

	time.Sleep(10 * time.Second)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Errorf("%+v", err)
		if err := r.Close(); err != nil {
			t.Errorf("%+v", err)
		}
	}

	for err := range connErrs {
		if !errors.Is(err, ErrClientClosed) {
			t.Errorf("%+v", err)
		}
	}
}

func testAddROSpec(t *testing.T) {
	conn, err := net.Dial("tcp", *readerAddr)
	if err != nil {
		t.Fatal(err)
	}

	if err := conn.SetDeadline(time.Now().Add(300 * time.Second)); err != nil {
		t.Fatal(err)
		return
	}

	r, err := NewClient(WithConn(conn))
	if err != nil {
		t.Fatal(err)
	}

	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- r.Connect()
	}()

	spec := AddROSpec{
		ROSpec: ROSpec{
			ROSpecID:           1,
			Priority:           0,
			ROSpecCurrentState: ROSpecStateDisabled,
			ROBoundarySpec: ROBoundarySpec{
				ROSpecStartTrigger: ROSpecStartTrigger{
					ROSpecStartTriggerType: ROStartTriggerImmediate,
				},
				ROSpecStopTrigger: ROSpecStopTrigger{
					ROSpecStopTriggerType: ROStopTriggerDuration,
					DurationTriggerValue:  milliSecs32(60000),
				},
			},
			AISpecs: []AISpec{{
				AntennaIDs: []AntennaID{0},
				AISpecStopTrigger: AISpecStopTrigger{
					AISpecStopTriggerType: AIStopTriggerNone,
				},
				InventoryParameterSpecs: []InventoryParameterSpec{{
					InventoryParameterSpecID: 1,
					AirProtocolID:            AirProtoEPCGlobalClass1Gen2,
				}},
			}},
		},
	}

	if js, err := json.MarshalIndent(spec, "", "\t"); err != nil {
		t.Error(err)
	} else {
		t.Logf("%s", js)
	}

	payload, err := spec.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%# 02x", payload[50:])

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mt, resp, err := r.SendMessage(ctx, MsgAddROSpec, payload)
	cancel()

	if err != nil {
		t.Error(err)
	} else if resp == nil {
		t.Error("expected non-nil response")
	}

	switch mt {
	default:
		t.Errorf("expected %v; got %v", MsgAddROSpecResponse, mt)
	case MsgErrorMessage:
		readErr(t, resp)
	case MsgAddROSpecResponse:
		var roRsp AddROSpecResponse
		if err := roRsp.UnmarshalBinary(resp); err != nil {
			t.Errorf("%+v", err)
			t.Logf("%# 02x", resp)
		} else if err := roRsp.LLRPStatus.Err(); err != nil {
			t.Error(err)
		} else {
			t.Logf("%+v", roRsp)
		}

		if r, err := json.MarshalIndent(roRsp, "", "\t"); err != nil {
			t.Error(err)
		} else {
			t.Log(string(r))
		}
	}

	enableSpec, err := (&EnableROSpec{ROSpecID: 1}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mt, resp, err = r.SendMessage(ctx, MsgEnableROSpec, enableSpec)
	cancel()
	if err != nil {
		t.Error(err)
	}

	switch mt {
	default:
		t.Errorf("expected %v; got %v", MsgEnableROSpecResponse, mt)
	case MsgErrorMessage:
		readErr(t, resp)
	case MsgEnableROSpecResponse:
		enableRsp := EnableROSpecResponse{}
		if err := enableRsp.UnmarshalBinary(resp); err != nil {
			t.Errorf("%+v", err)
			t.Logf("%# 02x", resp)
		} else if err := enableRsp.LLRPStatus.Err(); err != nil {
			t.Error(err)
		}
	}

	time.Sleep(10 * time.Second)

	disableRO(t, r)
	deleteRO(t, r)

	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Errorf("%+v", err)
		if err := r.Close(); err != nil {
			t.Errorf("%+v", err)
		}
	}

	for err := range connErrs {
		if !errors.Is(err, ErrClientClosed) {
			t.Errorf("%+v", err)
		}
	}
}

func enableAccSpec(t *testing.T, r *Client) {
	t.Helper()

	disableSpec, err := (&DisableROSpec{ROSpecID: 1}).MarshalBinary()
	if err != nil {
		t.Error(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mt, resp, err := r.SendMessage(ctx, MsgDisableROSpec, disableSpec)

	if err != nil {
		t.Error(err)
		return
	}

	switch mt {
	default:
		t.Errorf("expected %v; got %v", MsgDisableROSpecResponse, mt)
	case MsgErrorMessage:
		readErr(t, resp)
	case MsgDisableROSpecResponse:
		disableRsp := DisableROSpecResponse{}
		if err := disableRsp.UnmarshalBinary(resp); err != nil {
			t.Errorf("%+v", err)
			t.Logf("%# 02x", resp)
		} else if err := disableRsp.LLRPStatus.Err(); err != nil {
			t.Error(err)
		}
	}
}

func disableRO(t *testing.T, r *Client) {
	t.Helper()

	disableSpec, err := (&DisableROSpec{ROSpecID: 1}).MarshalBinary()
	if err != nil {
		t.Error(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mt, resp, err := r.SendMessage(ctx, MsgDisableROSpec, disableSpec)

	if err != nil {
		t.Error(err)
		return
	}

	switch mt {
	default:
		t.Errorf("expected %v; got %v", MsgDisableROSpecResponse, mt)
	case MsgErrorMessage:
		readErr(t, resp)
	case MsgDisableROSpecResponse:
		disableRsp := DisableROSpecResponse{}
		if err := disableRsp.UnmarshalBinary(resp); err != nil {
			t.Errorf("%+v", err)
			t.Logf("%# 02x", resp)
		} else if err := disableRsp.LLRPStatus.Err(); err != nil {
			t.Error(err)
		}
	}
}

func deleteRO(t *testing.T, r *Client) {
	t.Helper()

	deleteSpec, err := (&DeleteROSpec{ROSpecID: 1}).MarshalBinary()
	if err != nil {
		t.Error(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mt, resp, err := r.SendMessage(ctx, MsgDeleteROSpec, deleteSpec)

	if err != nil {
		t.Error(err)
		return
	}

	switch mt {
	default:
		t.Errorf("expected %v; got %v", MsgDeleteROSpecResponse, mt)
	case MsgErrorMessage:
		readErr(t, resp)
	case MsgDeleteROSpecResponse:
		rsp := DeleteROSpecResponse{}
		if err := rsp.UnmarshalBinary(resp); err != nil {
			t.Errorf("%+v", err)
			t.Logf("%# 02x", resp)
		} else if err := rsp.LLRPStatus.Err(); err != nil {
			t.Error(err)
		}
	}
}

func readErr(t *testing.T, resp []byte) {
	t.Helper()
	errMsg := &ErrorMessage{}
	if err := errMsg.UnmarshalBinary(resp); err != nil {
		t.Error(err)
		t.Logf("%# 02x", resp)
	} else if err := errMsg.LLRPStatus.Err(); err != nil {
		t.Logf("%+v", errMsg)
		t.Error(err)
	} else {
		if r, err := json.MarshalIndent(errMsg, "", "\t"); err != nil {
			t.Error(err)
		} else {
			t.Log(string(r))
		}
	}
}
