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
	// The first 10 attempts accumulate about a minute of total wait time,
	// after which each attempt occurs ~30 seconds after the previous.
	Quick = ExpBackOff{
		BackOff:  50 * time.Millisecond,
		Max:      30 * time.Second,
		Jitter:   true,
		KeepErrs: 10,
	}

	// Slow expects that failure conditions may take awhile to resolve.
	// The first 10 attempts accumulate about an hour of total wait time,
	// after which each subsequent event occurs ~30 minutes after the previous.
	Slow = ExpBackOff{
		BackOff:  5 * time.Second,
		Max:      30 * time.Minute,
		Jitter:   true,
		KeepErrs: 10,
	}

	// ErrRetriesExceeded is returned as the MainErr of an FError
	// when a Retry operation fails more times than permitted.
	ErrRetriesExceeded = errors.New("retries exceeded")

	// ErrWaitExceedsDeadline is returned if the wait time before the next attempt
	// exceeds a Deadline present on a context.Context associated with the operation.
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

// ExpBackOff is used to call a function multiple times,
// waiting an exponentially increasing amount of time between attempts.
//
// ExpBackOff holds parameters for use in its Retry method.
// Between attempts, Retry waits multiple of the BackOff duration,
// where the multiple increases exponentially with the number of attempts.
// The delay between attempts will never exceed Max.
//
// If Jitter is false, the multiple is simply increasing powers of 2.
// If Jitter is true, the multiple is a pseudo-random integer
// selected with equal probability from an interval twice the non-Jitter size,
// so that the expected value of both is the same.
//
// Concretely, if BackOff is 1s, the expected delay between attempt n and n+1 is
// 1s, 2s, 4s, 16s, etc., and the total wait up to attempt n is 1s, 3s, 7s, 15s, etc.
// With Jitter, these are the exact wait times (up to the OS-precision, of course),
// whereas with Jitter, these are the mean wait times.
// Changing the BackOff duration scales these linearly:
// a BackOff of 3s yields expected waits of 3s, 6s, 12s, etc.
//
// If KeepErrs is <=0, a failed Retry returns an *FError with only the MainError.
// If it's >0, it limits the maximum number of Other errors *FError records.
//
// Since the zero value has a Max duration of 0,
// it retries the function as fast as possible.
// The global package variables can be used for common Retry behavior,
// otherwise create a new ExpBackOff with values appropriate for your use.
type ExpBackOff struct {
	BackOff  time.Duration
	Max      time.Duration
	KeepErrs int
	Jitter   bool
}

// FError records errors accumulated during each execution of f.
// FError is only returned if every f() attempt fails or retries are canceled.
//
// If a call to one of the Retry methods results in a successful call of f(),
// any errors accumulated during the operation are discarded.
// Since it cannot be known whether or not f() will eventually be successful,
// this structure provides some history of those errors
// along with some stdlib-compatible methods for reviewing those errors.
//
// FError.MainErr records the main reason the Retry operation failed.
// This may be ErrRetriesExceeded, ErrWaitExceedsDeadline,
// the non-nil return value from calling Err() on a user-supplied context.Context,
// or a non-nil error returned by f() itself.
// Calls to FError.Unwrap return this value.
//
// If len(FError.Others) > 0, each error it holds is non-nil;
// however, it may contain fewer errors than the maximum number of attempts,
// if retries are canceled or if the capacity is limited by the retry mechanism.
// In the latter case, they may be in a different order then f() attempts.
type FError struct {
	MainErr   error
	Others    []error
	Attempts  int
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

// addErr adds newErr to the list of Others, possibly pushing out older errors.
// It increments its count of the total number of attempts,
// saturating it if addition would otherwise overflow.
func (e *FError) addErr(newErr error) {
	if e.Attempts+1 > 0 {
		e.Attempts++
	}

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

// Error returns a string describing the MainError that caused FError,
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
		eIdx++
		if eIdx >= len(e.Others) {
			eIdx = 0
		}

		err := e.Others[eIdx]
		errs[i] = fmt.Sprintf("attempt %d: %v", e.Attempts-i, err)
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

// nextWait returns the Duration that should occur between attempt n and n+1;
// that is, the parameter attempts represents the number of previously failed attempts.
//
// For all values <= 0, this returns 0.
//
// Otherwise, it's the min of the Max and some multiple of the BackOff,
// where the multiple is either simply 2^(attempts-1) (when Jitter is false)
// or it's a random value with expected value of 2^(attempts-1) (when Jitter is true).
// Note that if Jitter is true, it is valid for the returned duration to be 0.
func (ebo ExpBackOff) nextWait(attempts int) time.Duration {
	if attempts <= 0 || ebo.Max == 0 {
		return 0
	}

	if ebo.BackOff == 0 || attempts >= 63 {
		return ebo.Max
	}

	var wait time.Duration

	if ebo.Jitter {
		// Choose a random value in [0, 2^attempt) with uniform probability.
		// The expected value is (1/2)*2^attempt = 2^(attempt-1).
		s := time.Duration(rand.Int63n(1 << attempts))
		if s > 0 && ebo.BackOff > ebo.Max/s {
			wait = ebo.Max
		} else {
			wait = ebo.BackOff * s
		}
	} else if ebo.BackOff <= ebo.Max/(1<<(attempts-1)) {
		wait = ebo.BackOff * (1 << (attempts - 1))
	} else {
		wait = ebo.Max
	}

	return wait
}
