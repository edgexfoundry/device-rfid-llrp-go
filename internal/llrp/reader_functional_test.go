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
	"sync"
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

		t.Run("addROSpec", testGatherTagReads)

		t.Skip("collected data instead of running tests")
		return
	}

	for _, testConfig := range []struct {
		Incoming
		Outgoing
	}{
		{&GetReaderConfig{}, &GetReaderConfigResponse{}},
		{&GetReaderCapabilities{}, &GetReaderCapabilitiesResponse{}},
		{&GetROSpecs{}, &GetROSpecsResponse{}},
	} {
		testConfig := testConfig
		t.Run(testConfig.Incoming.Type().String(), func(t *testing.T) {
			r, _ := getFunctionalClient(t)
			errs := make(chan error)

			go func() {
				defer close(errs)
				errs <- r.Connect()
			}()
			sendAndCheck(t, r, testConfig.Outgoing, testConfig.Incoming)

			if err := r.Close(); err != nil {
				t.Error(err)
			}

			for err := range errs {
				t.Error(err)
			}
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
	r, cleanup := getFunctionalClient(t)
	defer cleanup()

	spec := NewROSpec()
	spec.SetPeriodic(5 * time.Second)

	sendAndCheck(t, r, &AddROSpec{*spec}, &AddROSpecResponse{})
	sendAndCheck(t, r, &EnableROSpec{ROSpecID: spec.ROSpecID}, &EnableROSpecResponse{})
	time.Sleep(10 * time.Second)
	sendAndCheck(t, r, &DisableROSpec{ROSpecID: spec.ROSpecID}, &DisableROSpecResponse{})
	sendAndCheck(t, r, &DeleteROSpec{ROSpecID: spec.ROSpecID}, &DeleteROSpecResponse{})
}

func getFunctionalClient(t *testing.T) (r *Client, cleanup func()) {
	conn, err := net.Dial("tcp", *readerAddr)
	if err != nil {
		t.Fatal(err)
	}

	// ec := &errorCollector{}

	opts := []ClientOpt{
		WithConn(conn),
		// WithMessageHandler(MsgErrorMessage, ec),
		WithTimeout(300 * time.Second),
	}

	if r, err = NewClient(opts...); err != nil {
		t.Fatal(err)
	}

	return r, nil
}

type testingLogger struct {
	*testing.T
}

func (tl testingLogger) Println(v ...interface{}) {
	tl.Log(v...)
}

func (tl testingLogger) Printf(format string, v ...interface{}) {
	tl.Logf(format, v...)
}

// errorCollector is a concurrency-safe error collector.
// By default, its checkErrs method will ignore
// LLRP's MsgVersionUnsupported (since it's assumed to come from version negotiation)
// and ErrClientClosed (since it's assumed to be the normal exit condition).
type errorCollector struct {
	errors []error
	mux    sync.Mutex

	// these only apply during checkErrs
	reportClientClosed       bool
	reportVersionUnsupported bool
}

func (teh *errorCollector) addErr(err error) {
	if err == nil {
		return
	}

	teh.mux.Lock()
	teh.errors = append(teh.errors, err)
	teh.mux.Unlock()
}

func (teh *errorCollector) checkErrs(t *testing.T) {
	t.Helper()
	teh.mux.Lock()
	defer teh.mux.Unlock()

	for _, err := range teh.errors {
		if !teh.reportVersionUnsupported {
			se := &StatusError{}
			if errors.As(err, &se) && se.Status == StatusMsgVerUnsupported {
				continue
			}
		}

		if !teh.reportClientClosed && errors.Is(err, ErrClientClosed) {
			continue
		}

		t.Errorf("%+v", err)
	}
}

// handleMessage can be set as a handler for LLRP ErrorMessages.
func (teh *errorCollector) handleMessage(_ *Client, msg Message) {
	em := &ErrorMessage{}
	if err := msg.Unmarshal(em); err != nil {
		teh.addErr(err)
		return
	}

	teh.addErr(em.LLRPStatus.Err())
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
		t.Error(err)
	}

	if testing.Verbose() {
		prettyPrint(t, in)
	}
}

func closeConn(t *testing.T, c *Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Shutdown(ctx); err != nil {
		t.Errorf("%+v", err)
		if err := c.Close(); err != nil {
			t.Errorf("%+v", err)
		}
	}
}
