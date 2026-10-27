// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package httpegress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutError struct{}

func (*timeoutError) Error() string { return "dial tcp: i/o timeout" }

var (
	reachable = roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	unreachable = roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, &timeoutError{}
	})
)

// withAge makes the process look age old for the rest of the test.
func withAge(t *testing.T, age time.Duration) {
	orig := started
	started = time.Now().Add(-age)
	t.Cleanup(func() { started = orig })
}

func TestReady(t *testing.T) {
	for _, tc := range []struct {
		name      string
		age       time.Duration
		transport http.RoundTripper
		wantErr   bool
	}{
		{"Reachable", 0, reachable, false},
		{"Unreachable", 0, unreachable, true},
		{"UnreachablePastGrace", grace, unreachable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAge(t, tc.age)
			err := ready(context.Background(), &http.Client{Transport: tc.transport})
			if (err != nil) != tc.wantErr {
				t.Errorf("ready() = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

func TestReadyHandler(t *testing.T) {
	withAge(t, 0)
	for _, tc := range []struct {
		name      string
		transport http.RoundTripper
		want      int
	}{
		{"Reachable", reachable, http.StatusNoContent},
		{"Unreachable", unreachable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ReadyHandler(&http.Client{Transport: tc.transport})(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != tc.want {
				t.Errorf("ReadyHandler() status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestAwaitReady(t *testing.T) {
	withAge(t, 0)
	var calls int
	c := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if calls++; calls < 2 {
			return unreachable(r)
		}
		return reachable(r)
	})}
	AwaitReady(context.Background(), c)
	if calls != 2 {
		t.Errorf("probes = %d, want 2 (one unreachable, then reachable)", calls)
	}
}

func TestAwaitReadyStopsWithContext(t *testing.T) {
	withAge(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		AwaitReady(ctx, &http.Client{Transport: unreachable})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AwaitReady did not return after its context ended")
	}
}
