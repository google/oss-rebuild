// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcbtest

import (
	"context"
	"io"

	"google.golang.org/api/cloudbuild/v1"
)

// MockClient implements gcb.Client for testing.
type MockClient struct {
	CreateBuildFunc      func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error)
	WaitForOperationFunc func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error)
	CancelOperationFunc  func(op *cloudbuild.Operation) error
}

func (mc *MockClient) CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
	return mc.CreateBuildFunc(ctx, project, build)
}

func (mc *MockClient) WaitForOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
	return mc.WaitForOperationFunc(ctx, op)
}

func (mc *MockClient) CancelOperation(op *cloudbuild.Operation) error {
	return mc.CancelOperationFunc(op)
}

// MockLogsClient implements gcb.LogsClient for testing.
type MockLogsClient struct {
	ReadBuildLogsFunc func(ctx context.Context, buildID string) (io.ReadCloser, error)
	ReadStepLogsFunc  func(ctx context.Context, buildID string, stepIndex int) (io.ReadCloser, error)
	ListStepLogsFunc  func(ctx context.Context, buildID string) (int, error)
}

func (mlc *MockLogsClient) ReadBuildLogs(ctx context.Context, buildID string) (io.ReadCloser, error) {
	return mlc.ReadBuildLogsFunc(ctx, buildID)
}

func (mlc *MockLogsClient) ReadStepLogs(ctx context.Context, buildID string, stepIndex int) (io.ReadCloser, error) {
	return mlc.ReadStepLogsFunc(ctx, buildID, stepIndex)
}

func (mlc *MockLogsClient) ListStepLogs(ctx context.Context, buildID string) (int, error) {
	return mlc.ListStepLogsFunc(ctx, buildID)
}
