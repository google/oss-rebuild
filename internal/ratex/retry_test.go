// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package ratex

import (
	"context"
	"testing"
	"time"

	"github.com/pkg/errors"
)

var errTerminal = errors.New("terminal")

func newTestRetrier(attempts int, retryable func(error) bool) Retrier {
	return Retrier{Limiter: NewBackoffLimiter(time.Nanosecond, time.Millisecond), Attempts: attempts, Retryable: retryable}
}

func TestDo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attempts  int
		retryable func(error) bool
		errs      []error // returned by fn in order, nil means success
		wantCalls int
		wantErr   bool
	}{
		{"SucceedsFirstTry", 3, nil, []error{nil}, 1, false},
		{"RetriesThenSucceeds", 3, nil, []error{errTerminal, errTerminal, nil}, 3, false},
		{"GivesUpAfterAttempts", 3, nil, []error{errTerminal, errTerminal, errTerminal}, 3, true},
		{
			name:      "StopsOnNonRetryable",
			attempts:  3,
			retryable: func(error) bool { return false },
			errs:      []error{errTerminal},
			wantCalls: 1,
			wantErr:   true,
		},
		// Attempts is a count, not a retry budget, so a non-positive value must
		// still run the work once rather than skipping it.
		{"NonPositiveAttemptsRunsOnce", 0, nil, []error{errTerminal}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			err := newTestRetrier(tc.attempts, tc.retryable).Do(context.Background(), func() error {
				calls++
				return tc.errs[min(calls-1, len(tc.errs)-1)]
			})
			if calls != tc.wantCalls {
				t.Errorf("fn called %d times, want %d", calls, tc.wantCalls)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestDoReturnsWorkErrorUnwrapped(t *testing.T) {
	// Callers classify the returned error, so it must survive the round trip.
	err := newTestRetrier(2, nil).Do(context.Background(), func() error { return errTerminal })
	if !errors.Is(err, errTerminal) {
		t.Errorf("err = %v, want it to wrap errTerminal", err)
	}
}

func TestDoAbortsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	err := newTestRetrier(3, nil).Do(ctx, func() error {
		calls++
		return nil
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 0 {
		t.Errorf("fn called %d times, want 0 (pacing must gate the first attempt too)", calls)
	}
}

func TestDoFeedsLimiter(t *testing.T) {
	// A retryable failure must widen the period. Otherwise the retry does
	// nothing about the condition that caused it.
	r := Retrier{Limiter: NewBackoffLimiter(time.Millisecond, time.Second), Attempts: 2}
	before := r.Limiter.CurrentPeriod()
	_ = r.Do(context.Background(), func() error { return errTerminal })
	if got := r.Limiter.CurrentPeriod(); got <= before {
		t.Errorf("period %v did not widen from %v after a retryable failure", got, before)
	}
}

func TestDoWithoutLimiter(t *testing.T) {
	// The zero Retrier is what a caller gets by leaving the field unset, so it
	// must run the work rather than panicking or looping on it.
	var calls int
	err := Retrier{Attempts: 3}.Do(context.Background(), func() error {
		calls++
		return errTerminal
	})
	if calls != 1 {
		t.Errorf("fn called %d times, want 1", calls)
	}
	if !errors.Is(err, errTerminal) {
		t.Errorf("err = %v, want errTerminal", err)
	}
}
