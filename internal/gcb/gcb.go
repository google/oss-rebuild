// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcb

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/api/cloudbuild/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Client interface abstracts Cloud Build service interactions.
type Client interface {
	CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error)
	WaitForOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error)
	CancelOperation(op *cloudbuild.Operation) error
}

// clientImpl is a concrete implementation of the Client interface using the Cloud Build service.
type clientImpl struct {
	service      *cloudbuild.Service
	pollInterval time.Duration
}

// NewClient creates a new Client with the given options.
func NewClient(s *cloudbuild.Service) Client {
	// TODO: Add optional configuration of poll value if/when needed.
	return &clientImpl{
		service:      s,
		pollInterval: 10 * time.Second, // default GCB API quota is low
	}
}

// CreateBuild creates and starts a GCB Build.
func (c *clientImpl) CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
	return c.service.Projects.Builds.Create(project, build).Context(ctx).Do()
}

// WaitForOperation polls and waits for the operation to complete.
// Operations.Get() will respect context deadlines, in which case that error will be returned
func (c *clientImpl) WaitForOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
	for !op.Done {
		select {
		// Wait for ctx.Done() in case a cancel is called during the pollInterval.
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.pollInterval):
			var err error
			op, err = c.service.Operations.Get(op.Name).Context(ctx).Do()
			if err != nil {
				return nil, errors.Wrap(err, "fetching operation")
			}
		}
	}
	return op, nil
}

func (c *clientImpl) CancelOperation(op *cloudbuild.Operation) error {
	_, err := c.service.Operations.Cancel(op.Name, &cloudbuild.CancelOperationRequest{}).Do()
	return err
}

// DoBuild executes a build on Cloud Build, waits for completion and returns the Build.
func DoBuild(ctx context.Context, client Client, project string, build *cloudbuild.Build) (*cloudbuild.Build, error) {
	initOp, err := client.CreateBuild(ctx, project, build)
	if err != nil {
		return nil, err
	}
	doneOp, err := client.WaitForOperation(ctx, initOp)
	if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("GCB deadline exceeded, cancelling build %s", initOp.Name)
		if err := client.CancelOperation(initOp); err != nil {
			log.Printf("Best effort GCB cancellation failed: %v", err)
			return nil, errors.Wrap(err, "cancelling operation")
		}
		// We can wait 10 more seconds for operation to be updated
		newCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if doneOp, err = client.WaitForOperation(newCtx, initOp); err != nil {
			return nil, errors.Wrap(err, "fetching operation after cancel")
		}
	} else if err != nil {
		// NOTE: We could potentially also cancel these unknown error cases, not just DeadlineExceeded
		return nil, errors.Wrap(err, "fetching operation")
	}
	// NOTE: Build status check will handle failures with better error messages.
	if doneOp.Error != nil {
		log.Printf("Cloud Build error: %v", status.Error(codes.Code(doneOp.Error.Code), doneOp.Error.Message))
	}
	var bm cloudbuild.BuildOperationMetadata
	if err := json.Unmarshal(doneOp.Metadata, &bm); err != nil {
		return nil, err
	}
	return bm.Build, nil
}

func ToError(build *cloudbuild.Build) error {
	switch build.Status {
	case "SUCCESS":
		return nil
	case "FAILURE":
		return errors.Errorf("GCB build failed: %s", build.StatusDetail)
	case "TIMEOUT":
		return errors.Errorf("GCB build timeout: %s", build.StatusDetail)
	case "CANCELLED":
		return errors.Errorf("GCB build cancelled: %s", build.StatusDetail)
	case "INTERNAL_ERROR", "EXPIRED":
		return errors.Errorf("GCB build internal error: %s", build.StatusDetail)
	default:
		return errors.Errorf("Unexpected build status: %s", build.Status)
	}

}

// NOTE: There are also per-step logs available as log-<id>-step-<n>.txt
func MergedLogFile(buildID string) string {
	return fmt.Sprintf("log-%s.txt", buildID)
}
