// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"testing"

	"github.com/google/oss-rebuild/internal/gcb/gcbtest"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/googleapi"
)

func TestDoBuildTimeout(t *testing.T) {
	opWasCancelled := false
	cancelChan := make(chan struct{}, 1)
	c := &gcbtest.MockClient{
		CreateBuildFunc: func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
			return &cloudbuild.Operation{Name: "name"}, nil
		},
		WaitForOperationFunc: func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
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
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	b, err := DoBuild(ctx, c, "project", &cloudbuild.Build{})
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
