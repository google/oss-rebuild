// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"errors"
	"os"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
)

type VersionDeps struct {
	InferenceVersionStub api.StubT[schema.VersionRequest, schema.VersionResponse]
}

func Version(ctx context.Context, req schema.VersionRequest, deps *VersionDeps) (*schema.VersionResponse, error) {
	switch req.Service {
	case "":
		return &schema.VersionResponse{Version: os.Getenv("K_REVISION")}, nil
	case "inference":
		return deps.InferenceVersionStub(ctx, req)
	default:
		return nil, api.AsStatus(codes.InvalidArgument, errors.New("unknown service"))
	}
}
