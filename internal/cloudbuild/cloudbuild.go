// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloudbuild

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/pkg/errors"
	cloudbuild "google.golang.org/api/cloudbuild/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Client interface abstracts Cloud Build service interactions.
type Client interface {
	CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error)
	GetOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error)
}

// Service is a concrete implementation using the Cloud Build service.
type Service struct {
	Service *cloudbuild.Service
}

// CreateBuild creates and starts a GCB Build.
func (cbs *Service) CreateBuild(ctx context.Context, project string, build *cloudbuild.Build) (*cloudbuild.Operation, error) {
	return cbs.Service.Projects.Builds.Create(project, build).Context(ctx).Do()
}

// GetOperation polls and returns the current state of a GCB operation.
func (cbs *Service) GetOperation(ctx context.Context, op *cloudbuild.Operation) (*cloudbuild.Operation, error) {
	return cbs.Service.Operations.Get(op.Name).Context(ctx).Do()
}

// DoBuild executes a build on Cloud Build, waits for completion, and updates the provided BuildInfo.
func DoBuild(ctx context.Context, client Client, project string, build *cloudbuild.Build) (*cloudbuild.Build, error) {
	op, err := client.CreateBuild(ctx, project, build)
	if err != nil {
		return nil, err
	}
	for !op.Done {
		time.Sleep(10 * time.Second)
		op, err = client.GetOperation(ctx, op)
		if err != nil {
			return nil, errors.Wrap(err, "fetching operation")
		}
	}
	// NOTE: Build status check will handle failures with better error messages.
	if op.Error != nil {
		log.Printf("Cloud Build error: %v", status.Error(codes.Code(op.Error.Code), op.Error.Message))
	}
	var bm cloudbuild.BuildOperationMetadata
	if err := json.Unmarshal(op.Metadata, &bm); err != nil {
		return nil, err
	}
	switch bm.Build.Status {
	case "SUCCESS":
	case "FAILURE", "TIMEOUT":
		return nil, errors.Errorf("GCB build failed: %s", bm.Build.StatusDetail)
	case "INTERNAL_ERROR", "CANCELLED", "EXPIRED":
		return nil, errors.Errorf("GCB build internal error: %s", bm.Build.StatusDetail)
	default:
		return nil, errors.Errorf("Unexpected build status: %s", bm.Build.Status)
	}
	return bm.Build, nil
}
