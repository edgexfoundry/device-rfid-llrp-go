//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"context"
	"flag"
	"github.com/pkg/errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// ex: go test -reader="localhost:5084"
// if using Goland, put that in the 'program arguments' part of the test config
var readerAddr = flag.String("reader", "", "address of an LLRP reader; enables functional tests")

func TestReader_withGolemu(t *testing.T) {
	addr := *readerAddr
	if addr == "" {
		t.Skip("no reader set for functional tests; use -test.reader=\"host:port\" to run")
	}

	t.Run("hangup", testHangUp)

	for i := 0; i < 10; i++ {
		t.Run("connect "+strconv.Itoa(i), testConnect)
	}
}

func BenchmarkReader_withGolemu(b *testing.B) {
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
	b := [headerSz]byte{}
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
		m, err := r.SendMessage(ctx, GetSupportedVersion, nil)
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
	_, err = r.SendMessage(ctx, GetSupportedVersion, nil)
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

	r, err := NewReader(WithConn(conn))
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
	var empty []byte

	resp, err := r.SendMessage(ctx, SetReaderConfig, empty)
	if err != nil {
		t.Error(err)
	} else if resp == nil {
		t.Error("expected non-nil response")
	}
	cancel()

	<-time.After(20 * time.Second)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		if err == context.DeadlineExceeded {
			if err := r.Close(); err != nil {
				t.Error(err)
			}
		} else {
			t.Errorf("%+v", err)
		}
	}

	for err := range connErrs {
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("%+v", err)
		}
	}
}
