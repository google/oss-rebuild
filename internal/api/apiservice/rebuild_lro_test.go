// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"testing"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/run/v2"
)

func TestCreateAndGetRebuildOp(t *testing.T) {
	ctx := context.Background()
	attempts := db.NewMemoryAttempts()

	deps := &CreateRebuildOpDeps{
		Attempts: attempts,
		RunJob: func(ctx context.Context, name string, req *run.GoogleCloudRunV2RunJobRequest) (*run.GoogleLongrunningOperation, error) {
			return &run.GoogleLongrunningOperation{Done: true}, nil
		},
	}

	req := schema.RebuildPackageRequest{
		Package:  "test-package",
		ID:       "run-id",
		Version:  "1.0.0",
		Artifact: "test.tar.gz",
	}

	op, err := CreateRebuildOp(ctx, req, deps)
	if err != nil {
		t.Fatalf("CreateRebuildOp failed: %v", err)
	}

	if op.ID == "" {
		t.Error("expected non-empty operation ID")
	}

	if op.Done {
		t.Error("expected operation to be not done initially")
	}

	// Test Get
	getDeps := &GetRebuildOpDeps{
		Reader: NewRebuildView(attempts),
	}
	got, err := GetRebuildOp(ctx, schema.GetOperationRequest{ID: op.ID}, getDeps)
	if err != nil {
		t.Fatalf("GetRebuildOp failed: %v", err)
	}

	if got.ID != op.ID {
		t.Errorf("expected ID %s, got %s", op.ID, got.ID)
	}

	// Simulate completion by updating the underlying resource
	key, err := toAttemptKey(op.ID)
	if err != nil {
		t.Fatalf("toAttemptKey failed: %v", err)
	}
	attempt, err := attempts.Get(ctx, key)
	if err != nil {
		t.Fatalf("attempts.Get failed: %v", err)
	}
	attempt.Status = schema.RebuildStatusSuccess
	attempt.Message = "success"
	if err := attempts.Update(ctx, attempt); err != nil {
		t.Fatalf("attempts.Update failed: %v", err)
	}

	got, err = GetRebuildOp(ctx, schema.GetOperationRequest{ID: op.ID}, getDeps)
	if err != nil {
		t.Fatalf("GetRebuildOp failed: %v", err)
	}

	if !got.Done {
		t.Error("expected operation to be done")
	}
	if got.Result == nil || got.Result.Message != "success" {
		t.Errorf("expected success result, got %+v", got.Result)
	}
}
