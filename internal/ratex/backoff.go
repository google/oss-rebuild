// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package ratex

import (
	"context"
	"sync"
	"time"
)

// BackoffLimiter provides a threadsafe exponential backoff rate limiter.
// Permits are spaced by the current period, which grows on Backoff and decays
// on Success within [minimum, maximum]. The first permit is immediate.
type BackoffLimiter struct {
	waitMu  sync.Mutex // serializes waiters
	mu      sync.Mutex // guards the fields below
	period  time.Duration
	minimum time.Duration
	maximum time.Duration
	last    time.Time // when the last permit was granted
}

func NewBackoffLimiter(minimum, maximum time.Duration) *BackoffLimiter {
	return &BackoffLimiter{period: minimum, minimum: minimum, maximum: max(maximum, minimum)}
}

// Wait blocks until the limiter permits another event to happen.
// If ctx becomes Done(), Wait will return an error.
func (l *BackoffLimiter) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.waitMu.Lock()
	defer l.waitMu.Unlock()
	l.mu.Lock()
	var wait time.Duration
	if !l.last.IsZero() {
		wait = l.period - time.Since(l.last)
	}
	l.mu.Unlock()
	if wait > 0 {
		// Period changes made during this sleep only affect later waits.
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	l.mu.Lock()
	l.last = time.Now()
	l.mu.Unlock()
	return nil
}

// Backoff will increase the period by 33%, up to the maximum.
func (l *BackoffLimiter) Backoff() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.period = min(l.period*4/3, l.maximum)
}

// Success will decrease the period by 10%, down to the minimum.
func (l *BackoffLimiter) Success() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.period = max(l.period*9/10, l.minimum)
}

func (l *BackoffLimiter) CurrentPeriod() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.period
}
