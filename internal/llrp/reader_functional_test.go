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
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ex: go test -reader="localhost:5084"
// if using Goland, put that in the 'program arguments' part of the test config
var readerAddr = flag.String("reader", "", "address of an LLRP reader; enables functional tests")
var update = flag.Bool("update", false, "rather than testing, record messages to the testdata directory")
var roDirectory = flag.String("ro-access-dir", "roAccessReports", "subdirectory of testdata for storing RO Access Reports")

func TestClientFunctional(t *testing.T) {
	addr := *readerAddr
	if addr == "" {
		t.Skip("no reader set for functional tests; use -test.reader=\"host:port\" to run")
	}

	if *update {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}

		t.Run("collectData", func(t *testing.T) {
			if err := collectData(); err != nil && !errors.Is(err, ErrClientClosed) {
				t.Fatal(err)
			}
		})

		t.Run("gatherTagReads", testGatherTagReads)

		t.Skip("collected data instead of running tests")
		return
	}

	for _, testConfig := range []struct {
		Outgoing
		Incoming
	}{
		{&GetReaderConfig{}, &GetReaderConfigResponse{}},
		{&GetReaderCapabilities{}, &GetReaderCapabilitiesResponse{}},
		{&GetROSpecs{}, &GetROSpecsResponse{}},
	} {
		testConfig := testConfig

		t.Run(testConfig.Incoming.Type().String(), func(t *testing.T) {
			r, cleanup := GetFunctionalClient(t, *readerAddr)
			defer cleanup()
			sendAndCheck(t, r, testConfig.Outgoing, testConfig.Incoming)
		})
	}

	t.Run("gatherTagReads", testGatherTagReads)
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

	return writeCapture("testdata", 0, result, resultT, resultValue)
}

func writeCapture(dir string, idx uint32, result []byte, typ MessageType, decoder encoding.BinaryUnmarshaler) error {
	baseName := fmt.Sprintf("%v-%03d", typ.String()[len("Msg"):], idx)
	bfn := filepath.Join(dir, baseName+".bytes")
	jfn := filepath.Join(dir, baseName+".json")

	if err := ioutil.WriteFile(bfn, result, 0644); err != nil {
		return err
	}

	if err := decoder.UnmarshalBinary(result); err != nil {
		return err
	}

	j, err := json.MarshalIndent(decoder, "", "\t")
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
		if err := r.Close(); err != nil {
			b.Error(err)
		}
	}
}

func testGatherTagReads(t *testing.T) {
	if testing.Short() {
		t.Skip("-short flag: skipping gather tag reads, since it takes 10s")
	}
	r, cleanup := GetFunctionalClient(t, *readerAddr)
	defer cleanup()

	spec := NewROSpec()
	spec.SetPeriodic(5 * time.Second)

	sendAndCheck(t, r, &AddROSpec{*spec}, &AddROSpecResponse{})
	sendAndCheck(t, r, &EnableROSpec{ROSpecID: spec.ROSpecID}, &EnableROSpecResponse{})
	time.Sleep(10 * time.Second)
	sendAndCheck(t, r, &DisableROSpec{ROSpecID: spec.ROSpecID}, &DisableROSpecResponse{})
	sendAndCheck(t, r, &DeleteROSpec{ROSpecID: spec.ROSpecID}, &DeleteROSpecResponse{})
}

func prettyPrint(t *testing.T, v interface{}) {
	t.Helper()
	if pretty, err := json.MarshalIndent(v, "", "\t"); err != nil {
		t.Errorf("can't pretty print %+v: %+v", v, err)
	} else {
		t.Logf("%s", pretty)
	}
}

func sendAndCheck(t *testing.T, c *Client, out Outgoing, in Incoming) {
	t.Helper()

	if testing.Verbose() {
		prettyPrint(t, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.SendFor(ctx, out, in); err != nil {
		t.Errorf("%+v", err)
	}

	if testing.Verbose() {
		prettyPrint(t, in)
	}
}
