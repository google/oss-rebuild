package gcbtest

import (
	"context"

	"github.com/google/oss-rebuild/internal/gcb"
	"google.golang.org/api/cloudbuild/v1"
)

// MockClient implements gcb.Client for testing.
type MockClient struct {
	CreateBuildFunc  func(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error)
	GetOperationFunc func(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error)
}

var _ gcb.Client = &MockClient{}

func (mc *MockClient) CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
	return mc.CreateBuildFunc(ctx, project, build)
}

func (mc *MockClient) GetOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
	return mc.GetOperationFunc(ctx, op)
}
