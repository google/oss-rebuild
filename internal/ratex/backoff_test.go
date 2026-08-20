// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package ratex

import (
	"context"
	"testing"
	"time"
)

func TestWaitFirstPermitImmediate(t *testing.T) {
	l := NewBackoffLimiter(time.Hour, 2*time.Hour)
	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("first Wait took %v, want immediate", d)
	}
}

func TestWaitUsesCurrentPeriod(t *testing.T) {
	// A Backoff between permits must apply to the very next wait, not lag
	// behind by one.
	l := NewBackoffLimiter(10*time.Millisecond, time.Second)
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	for range 3 {
		l.Backoff() // 10ms -> ~23.7ms
	}
	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	if d := time.Since(start); d < 20*time.Millisecond {
		t.Errorf("second Wait took %v, want at least the backed-off period (~23ms)", d)
	}
}

func TestWaitAbortsOnDoneContext(t *testing.T) {
	l := NewBackoffLimiter(time.Hour, 2*time.Hour)
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx); err == nil {
		t.Error("Wait() = nil, want context error instead of sleeping out the period")
	}
}

func TestBackoffCapsAtMaximum(t *testing.T) {
	l := NewBackoffLimiter(time.Millisecond, 3*time.Millisecond)
	for range 20 {
		l.Backoff()
	}
	if got := l.CurrentPeriod(); got != 3*time.Millisecond {
		t.Errorf("period = %v, want capped at 3ms", got)
	}
}

func TestSuccessFloorsAtMinimum(t *testing.T) {
	l := NewBackoffLimiter(time.Millisecond, time.Second)
	l.Backoff()
	for range 20 {
		l.Success()
	}
	if got := l.CurrentPeriod(); got != time.Millisecond {
		t.Errorf("period = %v, want floored at 1ms", got)
	}
}
