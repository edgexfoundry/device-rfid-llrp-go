//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"testing"
	"testing/quick"
	"time"
)

var (
	urerr  = fmt.Errorf("unrecoverable error")
	recerr = fmt.Errorf("recoverable error")
)

func TestExpBackOff_Retry_noErr(t *testing.T) {
	ebo := ExpBackOff{
		BackOff: 1 * time.Nanosecond,
		Max:     1 * time.Nanosecond,
	}

	if err := ebo.Retry(10, func() error { return nil }); err != nil {
		t.Error(err)
	}
}

func TestExpBackOff_Retry_errs(t *testing.T) {
	ebo := ExpBackOff{
		BackOff:  1 * time.Nanosecond,
		Max:      1 * time.Nanosecond,
		KeepErrs: 10,
	}

	// Retry expects all errors are recoverable.
	recoverable := func() error { return recerr }

	// Since the function reports it might recover,
	// it's called as many times as Retries allows;
	// the Latest error indicates RetriesExceeded.
	checkRetry(t, ebo, recoverable, 1, ErrRetriesExceeded, 1)
	checkRetry(t, ebo, recoverable, 2, ErrRetriesExceeded, 2)
	checkRetry(t, ebo, recoverable, 3, ErrRetriesExceeded, 3)

	// Even though this retries 15 times,
	// the EBO's setting tells Retry to only keep the last 10 errors.
	checkRetry(t, ebo, recoverable, 15, ErrRetriesExceeded, 10)
}

func TestExpBackOff_RetrySome(t *testing.T) {
	ebo := ExpBackOff{
		BackOff:  1 * time.Nanosecond,
		Max:      1 * time.Nanosecond,
		KeepErrs: 5,
	}

	recoverable := func() (bool, error) { return true, recerr }
	unrecoverable := func() (bool, error) { return false, urerr }

	checkRetrySome(t, ebo, recoverable, 1, ErrRetriesExceeded, 1)
	checkRetrySome(t, ebo, recoverable, 2, ErrRetriesExceeded, 2)
	checkRetrySome(t, ebo, recoverable, 3, ErrRetriesExceeded, 3)
	checkRetrySome(t, ebo, recoverable, 15, ErrRetriesExceeded, 5)
	checkRetrySome(t, ebo, unrecoverable, 1, urerr, 1)
	checkRetrySome(t, ebo, unrecoverable, 2, urerr, 1)
	checkRetrySome(t, ebo, unrecoverable, 3, urerr, 1)

	// Always return an error, but only return "true" the first few times.
	c := 0
	f := func() (bool, error) {
		c++
		if c > 3 {
			return false, urerr
		}
		return true, recerr
	}

	checkRetrySome(t, ebo, f, 5, urerr, 3)
}

type errType struct {
	e string
}

func (et *errType) Error() string {
	return et.e
}

func TestFError(t *testing.T) {
	ebo := ExpBackOff{KeepErrs: 5}

	et := &errType{e: "some error"}
	f := func() error { return et }
	err := ebo.Retry(2, f)

	if err == nil {
		t.Fatal("expected an error, but got nil")
	}

	if s := err.Error(); s == "" {
		t.Error("expected an error string, but got nothing")
	}

	// Unwrap returns the MainError, which should be ErrRetriesExceeded.
	if e2 := errors.Unwrap(err); e2 != ErrRetriesExceeded {
		t.Errorf("expected errors.Unwrap to be ErrRetriesExceeded, but got %+v", e2)
	}

	// The MainError is ErrRetriesExceeded.
	if !errors.Is(err, ErrRetriesExceeded) {
		t.Error("expected errors.Is(ErrRetriesExceeded) to succeed")
	}

	// All sub-errors are et.
	if !errors.Is(err, et) {
		t.Error("expected errors.Is(et) to succeed")
	}

	et2 := errors.Wrap(errors.Wrap(et, "another error"), "yet another")

	// Not all sub-errors are et2.
	if errors.Is(err, et2) {
		t.Error("expected errors.Is(et) to fail")
	}

	// It's not an empty FError.
	fe := &FError{}
	if errors.Is(err, fe) {
		t.Error("expected errors.Is(*FError) to fail")
	}

	// Now the FError partially matches, but has a different len for sub-errors.
	fe = &FError{
		MainErr: ErrRetriesExceeded,
		Others:  []error{et},
	}
	if errors.Is(err, fe) {
		t.Error("expected errors.Is(*FError) to fail")
	}

	// Now the lengths are equal, but not all the sub-errors match.
	fe.Others = append(fe.Others, et2)
	if errors.Is(err, fe) {
		t.Error("expected errors.Is(*FError) to fail")
	}

	// Now the main error and sub-errors should all match.
	fe.Others[1] = et
	if !errors.Is(err, fe) {
		t.Error("expected errors.Is(*FError) to succeed")
	}
}

func TestExpBackOff_RetryWithCtx(t *testing.T) {
	const tests, callsPerTest = 20, 10
	ebo := ExpBackOff{}

	for i := 0; i < tests; i++ {
		// f fails the first i times, but passes after that.
		f, count := untilCount(i)
		err := ebo.RetryWithCtx(context.Background(), callsPerTest, f)

		if i >= callsPerTest {
			if err == nil {
				t.Errorf("expected an error, but got nil")
			}

			if callsPerTest != *count {
				t.Errorf("expected %d failed attempts, but got %d", callsPerTest, *count)
			}
			continue
		}

		if err != nil {
			t.Errorf("unexpected error: %+v", err)
		} else if i != *count {
			t.Errorf("expected count to be %d; got %d", i, *count)
		}
	}
}

func TestExpBackOff_RetryWithCtx_expiredCtx(t *testing.T) {
	ebo := ExpBackOff{}

	ctx, cancel := context.WithCancel(context.Background())

	f, _ := untilCount(100)

	if err := ebo.RetryWithCtx(ctx, 10, f); err == nil {
		t.Errorf("expected an error, but didn't get one")
	} else if fe, ok := err.(*FError); !ok || fe == nil {
		t.Errorf("expected an FError, but got %+v", err)
	} else if fe.MainErr != ErrRetriesExceeded {
		t.Errorf("expected latest error to be %+v, but got %+v", ErrRetriesExceeded, fe.MainErr)
	}

	cancel()

	// Retry shouldn't even call f once.
	if err := ebo.RetryWithCtx(ctx, 10, func(_ context.Context) (bool, error) {
		t.Fatal("unexpected call to f")
		return true, nil
	}); err == nil {
		t.Error("expected an error, but didn't get one")
	} else if fe, ok := err.(*FError); !ok || fe == nil {
		t.Errorf("expected an FError, but got %+v", err)
	} else if fe.MainErr != ctx.Err() {
		t.Errorf("expected latest error to be %+v, but got %+v", ctx.Err(), fe.MainErr)
	} else if len(fe.Others) != 0 {
		t.Errorf("expected 0 other errors, but got %+v", fe.Others)
	}
}

func TestExpBackOff_RetryWithCtx_cancel(t *testing.T) {
	ebo := ExpBackOff{
		BackOff: time.Minute,
		Max:     time.Hour,
	}

	fCalled := make(chan struct{})
	f := func(_ context.Context) (bool, error) {
		close(fCalled)
		return true, recerr
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		errs <- ebo.RetryWithCtx(ctx, 10, f)
		close(errs)
	}()

	<-fCalled
	cancel()

	err := <-errs
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected %v; got %v", context.Canceled, err)
	}
}

func TestExpBackOff_RetryWithCtx_deadline(t *testing.T) {
	ebo := ExpBackOff{
		BackOff: time.Minute,
		Max:     time.Hour,
	}

	fCalled := make(chan struct{})
	f := func(_ context.Context) (bool, error) {
		close(fCalled)
		return true, recerr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	errs := make(chan error, 1)
	go func() {
		errs <- ebo.RetryWithCtx(ctx, 10, f)
		close(errs)
	}()

	<-fCalled

	err := <-errs
	if !(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrWaitExceedsDeadline)) {
		t.Errorf("expected %v or %v; got %v",
			context.DeadlineExceeded, ErrWaitExceedsDeadline, err)
	}
}

func TestExpBackOff_nextWait(t *testing.T) {
	checkEBO := func(jitter bool, attempt, bko, max, exp int) (ok bool) {
		bko &= 0x7fff_ffff_ffff_ffff
		max &= 0x7fff_ffff_ffff_ffff

		ebo := ExpBackOff{
			BackOff: time.Duration(bko),
			Max:     time.Duration(max),
			Jitter:  jitter,
		}

		w := ebo.nextWait(attempt)

		if attempt == 0 {
			if w != 0 {
				t.Errorf("ebo %+v: expected 0, got %d", ebo, w)
				return false
			}
			return true
		}

		if jitter {
			if w > time.Duration(exp) {
				t.Errorf("ebo %+v: expected %d, got %d", ebo, exp, w)
				return false
			}
			return true
		}

		if time.Duration(exp) != w {
			t.Errorf("ebo %+v: expected %d, got %d", ebo, exp, w)
			return false
		}

		return true
	}

	for _, tc := range []struct{ attempt, bko, max, exp int }{
		{0, 1, 50, 0},
		{1, 1, 50, 1},
		{2, 1, 50, 3},
		{3, 1, 50, 7},
		{4, 1, 50, 15},
		{5, 1, 50, 31},
		{6, 1, 50, 50},
		{-1, 1, 50, 50},
		{-2, 1, 50, 50},

		// backoff of 2
		{0, 2, 50, 0},
		{1, 2, 50, 2},
		{2, 2, 50, 6},
		{3, 2, 50, 14},
		{4, 2, 50, 30},
		{5, 2, 50, 50},
		{6, 2, 50, 50},
		{-1, 2, 50, 50},
		{-2, 2, 50, 50},

		// backoff of 0 always give max (except for attempt 0)
		{0, 0, 100, 0},
		{1, 0, 100, 100},
		{2, 0, 100, 100},
		{3, 0, 100, 100},
		{4, 0, 100, 100},
		{-1, 0, 100, 100},
		{-2, 0, 100, 100},

		// backoff huge
		{0, (1 << 63) - 1, 50, 0},
		{1, (1 << 63) - 1, 50, 50},
		{2, (1 << 63) - 1, 50, 50},
		{3, (1 << 63) - 1, 50, 50},
		{-1, (1 << 63) - 1, 50, 50},
		{-2, (1 << 63) - 1, 50, 50},
	} {
		checkEBO(false, tc.attempt, tc.bko, tc.max, tc.exp)
		checkEBO(true, tc.attempt, tc.bko, tc.max, tc.exp)
	}

	if err := quick.Check(func(attempt, backoff, max int) bool {
		exp := max & 0x7fff_ffff_ffff_ffff
		t1 := checkEBO(false, attempt, backoff, max, exp)
		t2 := checkEBO(true, attempt, backoff, max, exp)
		return t1 && t2
	}, nil); err != nil {
		t.Error(err)
	}
}

// untilCount returns a function f and an int pointer c.
// The first n times that f is called,
// it increments c and returns a "recoverable" error.
// Once c reaches max, f simply returns nil.
func untilCount(max int) (Func, *int) {
	c := 0
	return func(_ context.Context) (bool, error) {
		if c < max {
			c++
			return true, recerr
		}

		return false, nil
	}, &c
}

// checkRetry is a helper that calls Retry, expecting a particular error.
func checkRetry(t *testing.T, ebo ExpBackOff, f func() error, retries int, latest error, expOtherErrs int) {
	t.Helper()
	if err := ebo.Retry(retries, f); err == nil {
		t.Errorf("expected an error, but got nil")
	} else if fe, ok := err.(*FError); !ok || fe == nil {
		t.Errorf("expected an FError, but got %+v", err)
	} else if fe.MainErr != latest {
		t.Errorf("expected latest error to be %+v, but got %+v", latest, fe.MainErr)
	} else if len(fe.Others) != expOtherErrs {
		t.Errorf("expected %d other errors, but got %+v", expOtherErrs, fe.Others)
	}
}

// checkRetrySome is a helper that calls RetrySome, expecting a particular error.
func checkRetrySome(t *testing.T, ebo ExpBackOff, f func() (bool, error), retries int, latest error, expOtherErrs int) {
	t.Helper()
	if err := ebo.RetrySome(retries, f); err == nil {
		t.Errorf("expected an error, but got nil")
	} else if fe, ok := err.(*FError); !ok || fe == nil {
		t.Errorf("expected an FError, but got %+v", err)
	} else if !fe.Is(latest) {
		t.Errorf("expected error to be %+v, but got %+v", latest, fe)
	} else if len(fe.Others) != expOtherErrs {
		t.Errorf("expected %d other errors, but got %+v", expOtherErrs, fe.Others)
	}
}
