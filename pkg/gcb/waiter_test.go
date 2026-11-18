// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
)

type fakeFeed struct {
	events chan string
	errs   chan error
	closed atomic.Bool
}

func newTestWaiter() (*fakeFeed, *Waiter) {
	f := &fakeFeed{events: make(chan string, 16), errs: make(chan error, 1)}
	w := newWaiter(
		func(ctx context.Context, notify func(string)) error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case err := <-f.errs:
					return err
				case id := <-f.events:
					notify(id)
				}
			}
		},
		func() error {
			f.closed.Store(true)
			return nil
		},
	)
	return f, w
}

func TestWaiterSubscriptionConfig(t *testing.T) {
	got := waiterSubscription("proj", "projects/proj/topics/cloud-builds")
	if !strings.HasPrefix(got.Name, "projects/proj/subscriptions/"+WaiterSubscriptionPrefix) {
		t.Errorf("Name = %q, want prefix %q", got.Name, "projects/proj/subscriptions/"+WaiterSubscriptionPrefix)
	}
	want := &pb.Subscription{
		Name:                     got.Name,
		Topic:                    "projects/proj/topics/cloud-builds",
		AckDeadlineSeconds:       10,
		MessageRetentionDuration: durationpb.New(10 * time.Minute),
		ExpirationPolicy:         &pb.ExpirationPolicy{Ttl: durationpb.New(24 * time.Hour)},
		Filter:                   `attributes.status != "STATUS_UNKNOWN" AND attributes.status != "PENDING" AND attributes.status != "QUEUED" AND attributes.status != "WORKING"`,
	}
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("subscription mismatch (-want +got):\n%s", diff)
	}
	for _, tc := range []struct {
		name string
		cfg  WaiterConfig
	}{
		{name: "EmptyTopic", cfg: WaiterConfig{Topic: ""}},
		{name: "ShortTopic", cfg: WaiterConfig{Topic: "cloud-builds"}},
		{name: "NilClient", cfg: WaiterConfig{Topic: "projects/proj/topics/cloud-builds", Client: nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWaiter(context.Background(), tc.cfg); err == nil {
				t.Errorf("NewWaiter(%+v) = nil, want error", tc.cfg)
			}
		})
	}
}

func TestWaiterWakesWaitForOperation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		publishBefore bool
	}{
		{name: "PublishedDuringWatch"},
		{name: "PublishedBeforeWatch", publishBefore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feed, w := newTestWaiter()
			defer w.Close()
			var gets atomic.Int32
			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				gets.Add(1)
				rec := httptest.NewRecorder()
				rec.WriteString(`{"name":"operations/build/proj/b1","done":true,"metadata":{"build":{"id":"b1","status":"SUCCESS"}}}`)
				return rec.Result(), nil
			})}
			svc, err := cloudbuild.NewService(context.Background(), option.WithEndpoint("http://gcb.test"), option.WithHTTPClient(hc))
			if err != nil {
				t.Fatal(err)
			}
			c := NewClient(svc, WithWaiter(w)).(*clientImpl)
			c.queuedPoll = time.Hour
			c.runningPoll = time.Hour
			c.waiterPoll = time.Hour
			initOp := &cloudbuild.Operation{
				Name:     "operations/build/proj/b1",
				Metadata: googleapi.RawMessage(`{"build":{"id":"b1","status":"QUEUED"}}`),
			}
			if tc.publishBefore {
				w.notify("b1")
			} else {
				go func() {
					feed.events <- "other-build"
					time.Sleep(15 * time.Millisecond)
					feed.events <- "b1"
				}()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			doneOp, err := c.WaitForOperation(ctx, initOp)
			if err != nil {
				t.Fatalf("WaitForOperation: %v", err)
			}
			if !doneOp.Done || gets.Load() != 1 {
				t.Errorf("done = %v, gets = %d, want true and 1", doneOp.Done, gets.Load())
			}
			if err := w.Close(); err != nil || !feed.closed.Load() {
				t.Errorf("Close() = %v, closed = %v, want nil and true", err, feed.closed.Load())
			}
		})
	}
}

func TestWaiterRetriesAfterTransientGetFailure(t *testing.T) {
	feed, w := newTestWaiter()
	defer w.Close()
	var gets atomic.Int32
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		if gets.Add(1) == 1 {
			rec.WriteHeader(http.StatusServiceUnavailable)
			return rec.Result(), nil
		}
		rec.WriteString(`{"name":"operations/build/proj/b1","done":true,"metadata":{"build":{"id":"b1","status":"SUCCESS"}}}`)
		return rec.Result(), nil
	})}
	svc, err := cloudbuild.NewService(context.Background(), option.WithEndpoint("http://gcb.test"), option.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(svc, WithWaiter(w)).(*clientImpl)
	c.queuedPoll = time.Hour
	c.runningPoll = 15 * time.Millisecond
	c.waiterPoll = time.Hour
	feed.events <- "b1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initOp := &cloudbuild.Operation{
		Name:     "operations/build/proj/b1",
		Metadata: googleapi.RawMessage(`{"build":{"id":"b1","status":"QUEUED"}}`),
	}
	doneOp, err := c.WaitForOperation(ctx, initOp)
	if err != nil {
		t.Fatalf("WaitForOperation: %v", err)
	}
	if !doneOp.Done || gets.Load() != 2 {
		t.Errorf("done = %v, gets = %d, want true and 2", doneOp.Done, gets.Load())
	}
}

func TestWaiterFallsBackToPollingOnStreamFailure(t *testing.T) {
	feed, w := newTestWaiter()
	var gets atomic.Int32
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		if gets.Add(1) == 1 {
			rec.WriteString(`{"name":"operations/build/proj/b1","done":false,"metadata":{"build":{"id":"b1","status":"WORKING"}}}`)
			return rec.Result(), nil
		}
		rec.WriteString(`{"name":"operations/build/proj/b1","done":true,"metadata":{"build":{"id":"b1","status":"SUCCESS"}}}`)
		return rec.Result(), nil
	})}
	svc, err := cloudbuild.NewService(context.Background(), option.WithEndpoint("http://gcb.test"), option.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(svc, WithWaiter(w)).(*clientImpl)
	c.queuedPoll = time.Hour
	c.runningPoll = 15 * time.Millisecond
	c.waiterPoll = time.Hour
	wantErr := errors.New("stream broken")
	go func() {
		time.Sleep(15 * time.Millisecond)
		feed.errs <- wantErr
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initOp := &cloudbuild.Operation{
		Name:     "operations/build/proj/b1",
		Metadata: googleapi.RawMessage(`{"build":{"id":"b1","status":"QUEUED"}}`),
	}
	doneOp, err := c.WaitForOperation(ctx, initOp)
	if err != nil {
		t.Fatalf("WaitForOperation: %v", err)
	}
	if !doneOp.Done || gets.Load() != 2 {
		t.Errorf("done = %v, gets = %d, want true and 2", doneOp.Done, gets.Load())
	}
	if err := w.Close(); !errors.Is(err, wantErr) {
		t.Errorf("Close() = %v, want %v", err, wantErr)
	}
}

func TestWaiterPollsWithoutEvents(t *testing.T) {
	_, w := newTestWaiter()
	defer w.Close()
	var gets atomic.Int32
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		if gets.Add(1) == 1 {
			rec.WriteString(`{"name":"operations/build/proj/b1","done":false,"metadata":{"build":{"id":"b1","status":"WORKING"}}}`)
			return rec.Result(), nil
		}
		rec.WriteString(`{"name":"operations/build/proj/b1","done":true,"metadata":{"build":{"id":"b1","status":"SUCCESS"}}}`)
		return rec.Result(), nil
	})}
	svc, err := cloudbuild.NewService(context.Background(), option.WithEndpoint("http://gcb.test"), option.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(svc, WithWaiter(w)).(*clientImpl)
	c.queuedPoll = time.Hour
	c.runningPoll = time.Hour
	c.waiterPoll = 15 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	initOp := &cloudbuild.Operation{
		Name:     "operations/build/proj/b1",
		Metadata: googleapi.RawMessage(`{"build":{"id":"b1","status":"QUEUED"}}`),
	}
	doneOp, err := c.WaitForOperation(ctx, initOp)
	if err != nil {
		t.Fatalf("WaitForOperation: %v", err)
	}
	if !doneOp.Done || gets.Load() != 2 {
		t.Errorf("done = %v, gets = %d, want true and 2", doneOp.Done, gets.Load())
	}
}

func TestWaiterRecordsCompletions(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{name: "Fresh", want: true},
		{name: "Expired", age: completedBuildTTL + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, w := newTestWaiter()
			defer w.Close()
			w.completed["b1"] = time.Now().Add(-tc.age)
			for range 2 {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
				got := w.wait(ctx, "b1")
				cancel()
				if got != tc.want {
					t.Errorf("wait() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestDoBuildTimeoutTerminateWithWaiter(t *testing.T) {
	feed, w := newTestWaiter()
	defer w.Close()
	var gets atomic.Int32
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/builds"):
			rec.WriteString(`{"name":"operations/build/proj/b1","metadata":{"build":{"id":"b1","status":"QUEUED"}}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":cancel"):
			feed.events <- "b1"
			rec.WriteString(`{}`)
		case r.Method == http.MethodGet:
			gets.Add(1)
			rec.WriteString(`{"name":"operations/build/proj/b1","done":true,"metadata":{"build":{"id":"b1","status":"CANCELLED"}}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			rec.WriteHeader(http.StatusNotFound)
		}
		return rec.Result(), nil
	})}
	svc, err := cloudbuild.NewService(context.Background(), option.WithEndpoint("http://gcb.test"), option.WithHTTPClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(svc, WithWaiter(w))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	b, err := DoBuild(ctx, c, "proj", &cloudbuild.Build{}, DoBuildOpts{TerminateOnTimeout: true})
	if err != nil {
		t.Fatalf("DoBuild: %v", err)
	}
	if b.Status != "CANCELLED" || gets.Load() != 1 {
		t.Errorf("status = %q, gets = %d, want CANCELLED and 1", b.Status, gets.Load())
	}
}
