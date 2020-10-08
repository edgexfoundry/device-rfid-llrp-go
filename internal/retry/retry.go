//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

// Package retry provides utilities to retry an operation multiple times.
package retry

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	// Quick expects that failure conditions resolve quickly.
	Quick = ExpBackOff{
		BackOff:  50 * time.Millisecond,
		Max:      30 * time.Second,
		Jitter:   true,
		KeepErrs: 10,
	}

	// Slow expects that failure conditions may take awhile to resolve.
	Slow = ExpBackOff{
		BackOff:  5 * time.Second,
		Max:      30 * time.Minute,
		Jitter:   true,
		KeepErrs: 10,
	}

	ErrRetriesExceeded     = errors.New("retries exceeded")
	ErrWaitExceedsDeadline = errors.New("waiting would exceed the Deadline")
)

// Forever can be used as a number of retries to retry forever.
// If you retry forever, the number of errors can grow without bound.
const Forever = -1

// Func represents a function which can be retried.
// When it returns a non-nil error, it also returns a boolean
// which is true if the error is temporary (and the Func should be retried)
// or false if the error will never recover on its own.
type Func func(ctx context.Context) (bool, error)

// FError records errors accumulated during each execution of f.
// FError is only returned if every f() attempt fails or retries are canceled.
//
// FError.mainErr records the most recent error returned by f(),
// which may be the result of context.Error().
//
// If len(FError.Others) > 0, each error it holds is non-nil;
// however, it may contain fewer errors than the maximum number of attempts,
// if retries are canceled or if the capacity is limited by the retry mechanism.
// In the latter case, they may be in a different order then f() attempts.
type FError struct {
	MainErr   error
	Others    []error
	max, last int
}

// newFError returns an *FError that defaults to ErrRetriesExceeded.
func newFError(err error, maxErrs int) *FError {
	return &FError{
		MainErr: ErrRetriesExceeded,
		Others:  []error{err},
		last:    0,
		max:     maxErrs,
	}
}

// addErr adds newErr to the list of Others, possibly pushing out others.
// If e.max == 0, this has no impact.
func (e *FError) addErr(newErr error) {
	if e.max == 0 {
		return
	}

	if len(e.Others) < e.max {
		e.Others = append(e.Others, newErr)
		return
	}

	e.Others[e.last] = newErr
	e.last++
	if e.last >= len(e.Others) {
		e.last = 0
	}
}

// Unwrap returns the MainErr for this FError.
func (e *FError) Unwrap() error {
	return e.MainErr
}

// Error returns a string describing the MainError the caused FError,
// followed by any saved errors encountered during each Retry attempt,
// separated by newlines and indented by one tab.
func (e *FError) Error() string {
	if e == nil {
		return ""
	}

	if len(e.Others) == 0 {
		return e.MainErr.Error()
	}

	errs := make([]string, len(e.Others))
	eIdx := e.last
	for i := range e.Others {
		err := e.Others[eIdx]
		eIdx++
		if eIdx >= len(e.Others) {
			eIdx = 0
		}

		errs[i] = fmt.Sprintf("attempt %d: %v", i+1, err)
		for errors.Unwrap(err) != err {
			err = errors.Unwrap(err)
			if err == nil {
				break
			}
			errs[i] += fmt.Sprintf("\n\t\tdue to: %v", err)
		}
	}

	return fmt.Sprintf("%s after %d attempts:\n\t%s",
		e.MainErr.Error(), len(errs), strings.Join(errs, "\n\t"))
}

// Is tests whether FError matches a target.
//
// If the target is not an *FError,
// this returns true if MainErr matches the target,
// or if _all_ the Other errors in FError match the target,
// as determined by errors.Is(e.Others[i], target).
//
// If the target is an *FError, this compares their MainErr and Others errors.
// It returns true if errors.Is(e.MainErr, target.MainErr) is true,
// they have the same number of errors in Others,
// and errors.Is(e.Others[i], target.Others[i]) is true for all i.
func (e *FError) Is(target error) bool {
	fe, ok := target.(*FError)

	// If target is not an FError, either MainErr or Others must match.
	if !ok {
		// If MainErr matches target, it's a match.
		if errors.Is(e.MainErr, target) {
			return true
		}

		// Otherwise, _all_ Others must match.
		for _, err := range e.Others {
			if !errors.Is(err, target) {
				return false
			}
		}

		return true
	}

	// Since target is an FError, directly compare MainErr & Others.
	if !errors.Is(e.MainErr, fe.MainErr) || len(e.Others) != len(fe.Others) {
		return false
	}

	for i := range e.Others {
		if !errors.Is(e.Others[i], fe.Others[i]) {
			return false
		}
	}
	return true
}

// ExpBackOff is used to call a function multiple times,
// waiting an exponentially increasing amount of time between attempts.
//
// ExpBackOff holds parameters for use in its Retry method.
// Between attempts, Retry waits a multiple of BackOff,
// where the multiple is
// The multiple is based on the number of attempts and the value of jitter.
//
// If Jitter is true, then
// the multiple is a random int in [0, 1<<attempts);
// the expected delay between attempt n and n+1 is 0.5*BackOff*(pow(2, n)-1).
//
// If Jitter is false, then
// the multiple is just 1 << attempts;
// the exact delay between attempt n and n+1 is BackOff*(pow(2, n)-1).
//
// In either case, delay will never exceed Max.
//
// If KeepErrs is <=0, it only records the most recent error.
// If it's >0, it limits the maximum number of errors to record.
//
// The zero value retries the function as fast as possible
// and only records the most recent error.
type ExpBackOff struct {
	BackOff  time.Duration
	Max      time.Duration
	Jitter   bool
	KeepErrs int
}

// Retry attempts f up to retries number of times until it returns nil.
//
// If the program receives SIGINT or SIGKILL, the retries are canceled;
// use RetryWithCtx if you wish to control this behavior.
//
// If Retry returns non-nil, it is of type *FError.
func (ebo ExpBackOff) Retry(retries int, f func() error) error {
	return ebo.RetrySome(retries, func() (bool, error) { return true, f() })
}

// RetrySome retries f until one of the following is true:
// - f returns a nil error
// - f returns "false" for recoverable (indicating an unrecoverable error)
// - f has been called "retries" times
// - the program receives SIGINT or SIGKILL
//
// This last condition can be controlled by using RetryWithCtx instead.
//
// Errors received from f are wrapped in *FError.
func (ebo ExpBackOff) RetrySome(retries int, f func() (recoverable bool, err error)) error {
	run := func(_ context.Context) (bool, error) {
		return f()
	}

	osSig := make(chan os.Signal, 2)
	signal.Notify(osSig, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())

	defer func() {
		cancel()
		signal.Stop(osSig)
		close(osSig)
	}()

	go func() {
		select {
		case <-ctx.Done():
		case <-osSig:
			cancel()
		}
	}()

	return ebo.RetryWithCtx(ctx, retries, run)
}

// RetryWithCtx works like RetrySome, but allows a custom context,
// which may be used to cancel during waits between attempts.
// The context is passed to f unmodified.
//
// Regardless of the return values of f, if the context is canceled,
// Retry will not continue calling f;
// however, Retry can only check the context between calls to f.
// It is up to f to determine how/if to use the context.
// For short lived functions the don't await any signals,
// it's fine to ignore the context.
// For functions that block waiting on, e.g., a network resource,
// they should add ctx.Done to their select statements.
//
// In most cases, it's not really necessary for f to check ctx immediately.
// Before calls to f, Retry checks if ctx is canceled, past its Deadline,
// or if its Deadline would occur before the next call to f.
// In these cases, it adds ctx.Err to its accumulated errors and returns.
// As a result, at the beginning of f's execution,
// it's unlikely (though possible) that ctx is canceled or past its deadline.
func (ebo ExpBackOff) RetryWithCtx(ctx context.Context, retries int, f Func) error {
	// Check if the context is already expired.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return &FError{MainErr: ctxErr}
	}

	// if f is successful, avoid all the other work
	cont, err := f(ctx)
	if err == nil {
		return nil
	}

	// the retry error to collect f's errors; we'll return this if f never returns non-nil
	// re := &FError{Latest: err}
	re := newFError(err, ebo.KeepErrs)
	if !cont {
		re.MainErr = ErrRetriesExceeded
		return re
	}

	if ebo.Max <= 0 {
		ebo.Max = math.MaxInt64
	}

	if ebo.BackOff <= 0 {
		ebo.BackOff = 1
	}

	// Create a timer, but immediately stop it (and drain it if necessary).
	// Go Timers have a bit of an annoying API,
	// and they require a lot of care to prevent resource leaks and deadlock.
	delay := time.NewTimer(ebo.BackOff)
	if !delay.Stop() {
		<-delay.C
	}

	deadline, hasDeadline := ctx.Deadline()

	// start at 1 since we've already tried once
	for attempt := 1; retries == Forever || attempt < retries; attempt++ {
		wait := ebo.nextWait(attempt)
		if hasDeadline && time.Now().Add(wait).After(deadline) {
			re.MainErr = ErrWaitExceedsDeadline
			return re
		}

		delay.Reset(wait)

		// wait for next attempt
		select {
		case <-ctx.Done():
			re.MainErr = ctx.Err()
			if !delay.Stop() {
				<-delay.C
			}
			return re
		case <-delay.C:
		}

		// try the operation
		cont, err = f(ctx)
		if err == nil {
			return nil
		}

		if !cont {
			re.MainErr = err
			return re
		}

		re.addErr(err)
	}

	re.MainErr = ErrRetriesExceeded
	return re
}

func (ebo ExpBackOff) nextWait(attempt int) time.Duration {
	if attempt == 0 {
		return 0
	}

	var wait time.Duration

	if ebo.Jitter {
		// Choose a random value in [1, pow(2, attempts)-1]
		// and multiply it by the BackOff value.
		n := attempt
		if attempt >= 64 || attempt < 0 {
			n = 63
		}

		wait = ebo.BackOff * time.Duration(1+rand.Int63n(int64(1<<n)-1))
	} else if 0 < attempt && attempt <= 63 {
		wait = ebo.BackOff * ((1 << attempt) - 1)
	} else {
		wait = ebo.Max
	}

	// Handle overflow, and cutoff at the max value.
	if !(0 < wait && wait <= ebo.Max) {
		wait = ebo.Max
	}

	return wait
}
