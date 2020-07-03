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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ex: go test -reader="localhost:5084"
// if using Goland, put that in the 'program arguments' part of the test config
var readerAddr = flag.String("reader", "", "address of an LLRP reader; enables functional tests")
var update = flag.Bool("update", false, "rather than testing, record messages to the testdata directory")

func TestReaderFunctional(t *testing.T) {
	addr := *readerAddr
	if addr == "" {
		t.Skip("no reader set for functional tests; use -test.reader=\"host:port\" to run")
	}

	if *update {
		if err := collectData(); err != nil && !errors.Is(err, ErrReaderClosed) {
			t.Fatal(err)
		}
		t.Skip("collected data instead of running tests")
		return
	}

	t.Run("hangup", testHangUp)
	t.Run("addROSpec", testGatherTagReads)

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

	r, err := NewReader(WithConn(conn))
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
		if err := getAndWrite(r, GetSupportedVersion, nil, &getSupportedVersionResponse{}); err != nil {
			return err
		}
	}

	var errs []error
	for _, toSend := range []struct {
		mt  MessageType
		out encoding.BinaryMarshaler
		in  encoding.BinaryUnmarshaler
	}{
		{GetReaderConfig, &getReaderConfig{}, &getReaderConfigResponse{}},
		{GetReaderCapabilities, &getReaderCapabilities{}, &getReaderCapabilitiesResponse{}},
		{GetROSpecs, nil, &getROSpecsResponse{}},
		{GetAccessSpecs, nil, &getAccessSpecsResponse{}},
		{GetReport, nil, &roAccessReport{}},
		{CloseConnection, nil, &closeConnectionResponse{}},
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

func getAndWrite(r *Reader, mt MessageType, payload encoding.BinaryMarshaler, resultValue encoding.BinaryUnmarshaler) error {
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

	return writeCapture(0, result, resultT, resultValue)
}

func writeCapture(idx uint32, result []byte, typ MessageType, decoder encoding.BinaryUnmarshaler) error {
	bfn := fmt.Sprintf("testdata/%v-%03d.bytes", typ, idx)
	jfn := fmt.Sprintf("testdata/%v-%03d.json", typ, idx)

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

func BenchmarkReaderFunctional(b *testing.B) {
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

	r, err := NewReader(WithConn(conn), WithVersion(Version1_1), WithLogger(devNullLog{}))
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
			if !errors.Is(err, ErrReaderClosed) {
				b.Fatalf("%+v", err)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mt, m, err := r.SendMessage(ctx, GetSupportedVersion, nil)
		if GetSupportedVersionResponse != mt {
			b.Errorf("expected %v; got %v", GetSupportedVersionResponse, mt)
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

	r, err := NewReader(WithConn(conn), WithVersion(Version1_1), WithLogger(devNullLog{}))
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
			if !errors.Is(err, ErrReaderClosed) {
				b.Fatalf("%+v", err)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = r.SendMessage(ctx, GetSupportedVersion, nil)
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

	r, err := NewReader(WithConn(conn), WithTimeout(120*time.Second), WithLogger(testingLogger{T: t}))
	if err != nil {
		t.Fatal(err)
	}

	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- r.Connect()
	}()

	sendAndCheck(t, r, &getReaderConfig{}, &getReaderConfigResponse{})
	closeConn(t, r)

	for err := range connErrs {
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("%+v", err)
		}
	}
}

func testGatherTagReads(t *testing.T) {
	conn, err := net.Dial("tcp", *readerAddr)
	if err != nil {
		t.Fatal(err)
	}

	ec := &errorCollector{}

	opts := []ReaderOpt{
		WithConn(conn),
		WithLogger(testingLogger{T: t}),
		WithMessageHandler(ErrorMessage, ec),
		WithTimeout(300 * time.Second),
	}

	if *update {
		var i uint32
		opts = append(opts, WithMessageHandler(ROAccessReport, messageHandlerFunc(
			func(r *Reader, msg Message) {
				atomic.AddUint32(&i, 1)

				data, err := msg.data()
				if err != nil {
					ec.addErr(err)
					return
				}

				ec.addErr(writeCapture(i, data, ROAccessReport, &roAccessReport{}))
			})))
	}

	r, err := NewReader(opts...)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		ec.addErr(r.Connect())
	}()

	spec := NewROSpec()

	sendAndCheck(t, r, &addROSpec{*spec}, &addROSpecResponse{})
	sendAndCheck(t, r, &enableROSpec{ROSpecID: spec.ROSpecID}, &enableROSpecResponse{})

	// give it some time to send messages
	time.Sleep(10 * time.Second)

	sendAndCheck(t, r, &disableROSpec{ROSpecID: spec.ROSpecID}, &disableROSpecResponse{})
	sendAndCheck(t, r, &deleteROSpec{ROSpecID: spec.ROSpecID}, &deleteROSpecResponse{})

	closeConn(t, r)

	ec.checkErrs(t)
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
// and ErrReaderClosed (since it's assumed to be the normal exit condition).
type errorCollector struct {
	errors []error
	mux    sync.Mutex

	// these only apply during checkErrs
	reportReaderClosed       bool
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
			se := &statusError{}
			if errors.As(err, &se) && se.Status == StatusMsgVerUnsupported {
				continue
			}
		}

		if !teh.reportReaderClosed && errors.Is(err, ErrReaderClosed) {
			continue
		}

		t.Errorf("%+v", err)
	}
}

// handleMessage can be set as a handler for LLRP ErrorMessages.
func (teh *errorCollector) handleMessage(_ *Reader, msg Message) {
	em := &errorMessage{}
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

func sendAndCheck(t *testing.T, r *Reader, out outgoing, in incoming) {
	t.Helper()
	prettyPrint(t, out)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := r.sendFor(ctx, out, in); err != nil {
		t.Error(err)
	}

	prettyPrint(t, in)
}

func closeConn(t *testing.T, r *Reader) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Errorf("%+v", err)
		if err := r.Close(); err != nil {
			t.Errorf("%+v", err)
		}
	}
}
