//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

func TestReader_withGolemu(t *testing.T) {
	conn, err := net.Dial("tcp", "localhost:5084")
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

	wg := sync.WaitGroup{}
	wg.Add(1)
	errs := make(chan error, 1)
	go func() {
		defer wg.Done()
		errs <- r.Connect()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var empty []byte
	resp, err := r.SendMessage(ctx, empty, SetReaderConfig)
	if err != nil {
		t.Error(err)
	} else if resp == nil {
		t.Error("expected non-nil response")
	}

	<-time.After(10 * time.Second)
	cancel()

	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		if err == context.DeadlineExceeded {
			if err := r.Close(); err != nil {
				t.Error(err)
			}
		}
		t.Error(err)
	}
	wg.Wait()

	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("%+v", err)
		}
	}
}
