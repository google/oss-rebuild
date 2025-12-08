// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package ratex

import (
	"context"
	"sync"
	"time"
)

// BackoffLimiter provides a threadsafe exponential backoff rate limiter
type BackoffLimiter struct {
	mu            sync.Mutex
	currentPeriod time.Duration
	minimum       time.Duration
	ch            chan struct{}
}

func NewBackoffLimiter(minimum time.Duration) *BackoffLimiter {
	l := &BackoffLimiter{
		currentPeriod: minimum,
		minimum:       minimum,
		ch:            make(chan struct{}),
	}
	go func() {
		for {
			l.tick()
		}
	}()
	return l
}

func (l *BackoffLimiter) tick() {
	l.mu.Lock()
	duration := l.currentPeriod
	l.mu.Unlock()
	// If we want the in-flight sleep period to be interrupted, we can implement this with a time.AfterFunc(), and cancel the in-flight timer when the period is updated.
	time.Sleep(duration)
	l.ch <- struct{}{}
}

// Wait blocks until the limiter permits another event to happen.
// If ctx becomes Done(), Wait will return an error.
func (l *BackoffLimiter) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ch:
		return nil
	}
}

// Backoff will increase the period by 33%.
// This will not take effect until the next period.
func (l *BackoffLimiter) Backoff() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.currentPeriod = l.currentPeriod * 4 / 3
}

// Success will decrease the period by 10%.
// This will not take effect until the next period.
func (l *BackoffLimiter) Success() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.currentPeriod = max(l.currentPeriod*9/10, l.minimum)
}

func (l *BackoffLimiter) CurrentPeriod() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.currentPeriod
}
