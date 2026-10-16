// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/oss-rebuild/pkg/gcb/gcbtest"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func TestStepSection(t *testing.T) {
	log := `starting build "abc"
BUILD
Starting Step #0
Step #0: hello
Step #0: world
Finished Step #0
Starting Step #1 - "timing"
Step #1 - "timing": 2026-01-01T00:00:00.0Z 2026-01-01T00:01:00.0Z
Step #1 - "timing": {"CreatedAt":"2026-01-01T00:00:30Z"}
Finished Step #1 - "timing"
Step #10: tail without newline`
	for _, tc := range []struct {
		name string
		step int
		want string
	}{
		{
			name: "unnamed step",
			step: 0,
			want: "hello\nworld\n",
		},
		{
			name: "step with id",
			step: 1,
			want: "2026-01-01T00:00:00.0Z 2026-01-01T00:01:00.0Z\n{\"CreatedAt\":\"2026-01-01T00:00:30Z\"}\n",
		},
		{
			name: "multi-digit index not matched by prefix digit",
			step: 10,
			want: "tail without newline",
		},
		{
			name: "absent step",
			step: 2,
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stepSection(strings.NewReader(log), tc.step)
			if err != nil {
				t.Fatalf("stepSection unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("stepSection = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDoBuildTimeoutTerminate(t *testing.T) {
	opWasCancelled := false
	cancelChan := make(chan struct{}, 1)
	c := &gcbtest.MockClient{
		CreateBuildFunc: func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
			return &cloudbuild.Operation{Name: "name", Metadata: []byte(`{"build": {"id":"123"}}`)}, nil
		},
		WaitForOperationFunc: func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
			select {
			case <-ctx.Done():
				return op, ctx.Err()
			case <-cancelChan:
				return &cloudbuild.Operation{Name: "name", Done: true, Metadata: googleapi.RawMessage([]byte(`{"build":{"id":"123", "status":"CANCELLED"}}`))}, nil
			}
		},
		CancelOperationFunc: func(op *cloudbuild.Operation) error {
			opWasCancelled = true
			cancelChan <- struct{}{}
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	b, err := DoBuild(ctx, c, "project", &cloudbuild.Build{}, DoBuildOpts{TerminateOnTimeout: true})
	if err != nil {
		t.Errorf("DoBuild unexpected error: %v", err)
	}
	if !opWasCancelled {
		t.Errorf("DoBuild did not cancel operation")
	}
	if b == nil || b.Status != "CANCELLED" {
		t.Error("DoBuild did not return the updated build object")
	}
}

func TestDoBuildTimeoutNoTerminate(t *testing.T) {
	opWasCancelled := false
	cancelChan := make(chan struct{}, 1)
	c := &gcbtest.MockClient{
		CreateBuildFunc: func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
			return &cloudbuild.Operation{Name: "name", Metadata: []byte(`{ "build": {"id":"123"}}`)}, nil
		},
		WaitForOperationFunc: func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
			select {
			case <-ctx.Done():
				return &cloudbuild.Operation{Name: "updated name"}, ctx.Err()
			case <-cancelChan:
				return &cloudbuild.Operation{Name: "name", Done: true, Metadata: googleapi.RawMessage([]byte(`{"build":{"status":"CANCELLED"}}`))}, nil
			}
		},
		CancelOperationFunc: func(op *cloudbuild.Operation) error {
			opWasCancelled = true
			cancelChan <- struct{}{}
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	b, err := DoBuild(ctx, c, "project", &cloudbuild.Build{}, DoBuildOpts{TerminateOnTimeout: false})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("DoBuild expected DeadlineExceeded: got %v", err)
	}
	if opWasCancelled {
		t.Errorf("DoBuild unexpectedly cancelled the operation")
	}
	if b == nil || b.Id != "123" {
		t.Error("DoBuild did not return the updated build object")
	}
}

func TestToErrorReadsFailureInfo(t *testing.T) {
	b := &cloudbuild.Build{Status: "FAILURE", FailureInfo: &cloudbuild.FailureInfo{Type: "USER_BUILD_STEP", Detail: "step exited with non-zero status: 1"}}
	if got, want := ToError(b).Error(), "GCB build failed: USER_BUILD_STEP: step exited with non-zero status: 1"; got != want {
		t.Errorf("ToError() = %q, want %q", got, want)
	}
	// StatusDetail is not consulted.
	b = &cloudbuild.Build{Status: "TIMEOUT", StatusDetail: "ignored"}
	if got, want := ToError(b).Error(), "GCB build timeout: "; got != want {
		t.Errorf("ToError() = %q, want %q", got, want)
	}
}

func TestWaitForOperationRidesOutFailedPolls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int   // the first poll's response status
		err    error // or the first poll's transport error
	}{
		{"ConnectionReset", 0, syscall.ECONNRESET},
		{"RateLimited", http.StatusTooManyRequests, nil},
		{"Unavailable", http.StatusServiceUnavailable, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var polls int
			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				polls++
				if polls == 1 && tc.err != nil {
					return nil, tc.err
				}
				rec := httptest.NewRecorder()
				if polls == 1 {
					rec.WriteHeader(tc.status)
				} else {
					rec.WriteString(`{"name":"operations/op","done":true}`)
				}
				return rec.Result(), nil
			})}
			svc, err := cloudbuild.NewService(context.Background(), option.WithEndpoint("http://gcb.test"), option.WithHTTPClient(hc))
			if err != nil {
				t.Fatal(err)
			}
			c := &clientImpl{service: svc, pollInterval: time.Millisecond}
			op, err := c.WaitForOperation(context.Background(), &cloudbuild.Operation{Name: "operations/op"})
			if err != nil {
				t.Fatalf("WaitForOperation: %v", err)
			}
			if !op.Done || polls != 2 {
				t.Errorf("op.Done = %v after %d polls, want done after 2", op.Done, polls)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
