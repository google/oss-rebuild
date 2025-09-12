// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/gcb/gcbtest"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/googleapi"
)

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
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
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
