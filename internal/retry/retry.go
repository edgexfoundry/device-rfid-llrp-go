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
		Max:      5 * time.Minute,
		Jitter:   true,
		KeepErrs: 10,
	}
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
// FError.Latest records the most recent error returned by f(),
// which may be the result of context.Error().
//
// If len(FError.Others) > 0, each error it holds is non-nil;
// however, it may contain fewer errors than the maximum number of attempts,
// if retries are canceled or if the capacity is limited by the retry mechanism.
// In the latter case, they may be in a different order then f() attempts.
type FError struct {
	Latest error
	Others []error
}

// Error returns a string describing the first error encountered during Retry;
// it appends any errors encountered during attempts to the message,
// separated by newlines and indented by one tab.
func (e *FError) Error() string {
	if e == nil {
		return ""
	}

	if len(e.Others) == 0 {
		return e.Latest.Error()
	}

	errs := make([]string, len(e.Others))
	for i, e := range e.Others {
		errs[i] = fmt.Sprintf("attempt %d: %v", i+1, e)
		for errors.Cause(e) != e {
			e = errors.Cause(e)
			if e == nil {
				break
			}
			errs[i] += fmt.Sprintf("\n\t\tdue to: %v", e)
		}
	}
	return fmt.Sprintf("%s after %d attempts:\n\t%s",
		e.Latest.Error(), len(errs), strings.Join(errs, "\n\t"))
}

// As tests whether this FError matches the target.
//
// If target is a **FError, As sets it and returns true.
//
// If not, then tests As on _all_ of the sub Errors,
// and only returns true if _all_ of those return true.
// In that case, target is set to the final error in Errors.
//
// If any of the sub errors fail to match the target, this returns false.
func (e *FError) As(target interface{}) bool {
	if e == nil {
		return false
	}

	if re, ok := target.(*FError); ok {
		*re = *e
		return true
	}

	if len(e.Others) == 0 {
		return false
	}

	for _, err := range e.Others {
		if !errors.As(err, target) {
			return false
		}
	}

	return true
}

// ExpBackOff is used to call a function multiple times,
// waiting an exponentially increasing amount of time between attempts,
//
// Between attempts, Retry waits a multiple of BackOff.
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
// If Retry returns non-nil, it is of type *retry.Error.
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
// Errors received from f are wrapped in *retry.Error.
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

// RetryWithCtx works like f, but allows a custom context,
// which is passed to f unmodified.
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
	return Try(ctx, retries, ebo.KeepErrs, ebo.BackOff, ebo.Max, ebo.Jitter, f)
}

func addErr(attempt, maxErrs int, fErr *FError, newErr error) {
	if fErr.Latest != nil {
		if len(fErr.Others) < maxErrs {
			fErr.Others = append(fErr.Others, newErr)
		} else if maxErrs > 0 {
			idx := attempt % len(fErr.Others)
			fErr.Others[idx] = fErr.Latest
		}
	}

	fErr.Latest = newErr
}

// Try attempts f up to retries times until it returns false or nil,
// delaying by a multiple of backOff between attempts, up to the max delay.
//
// If the return is non-nil, it is of type *retry.Error.
//
// See ExpBackOff and the package instances of ExpBackOff for more information
// and an easier-to-use API.
func Try(ctx context.Context, retries, maxErrs int, backOff, max time.Duration, jitter bool, f Func) error {
	// if f is successful, avoid all the other work
	cont, err := f(ctx)
	if err == nil {
		return nil
	}

	// the retry error to collect f's errors; we'll return this if f never returns non-nil
	re := &FError{Latest: err}
	if !cont {
		return re
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		addErr(0, maxErrs, re, ctx.Err())
		return re
	}

	wait := backOff
	delay := time.NewTimer(wait)
	defer func() {
		if !delay.Stop() {
			<-delay.C
		}
	}()

	deadline, hasDeadline := ctx.Deadline()

	// start at 1 since we've already tried once
	for attempt := 1; retries == Forever || attempt < retries; attempt++ {
		if hasDeadline && time.Now().Add(wait).After(deadline) {
			addErr(attempt, maxErrs, re, errors.New("waiting would exceed Deadline"))
			return re
		}

		// wait for next attempt
		select {
		case <-ctx.Done():
			addErr(attempt, maxErrs, re, ctx.Err())
			return re
		case <-delay.C:
		}

		// try the operation
		cont, err = f(ctx)
		if err == nil {
			return nil
		}

		addErr(attempt, maxErrs, re, err)
		if !cont {
			return re
		}

		if jitter {
			n := int64(1 << attempt)
			if n <= 2 { // handles overflow as a side benefit
				n = 2
			}
			wait = backOff * time.Duration(rand.Int63n(n))
		} else {
			wait = backOff * (1 << attempt)
		}

		// handle overflow and max
		if wait <= 0 || (max > 0 && wait > max) {
			wait = max
		}

		delay.Reset(wait)
	}

	addErr(0, maxErrs, re, errors.New("retries exceeded"))
	return re
}
