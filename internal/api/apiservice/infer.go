// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"

	"github.com/google/oss-rebuild/internal/api/inferenceservice"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// InferDeps carries the inference services for the standalone infer endpoint.
type InferDeps struct {
	Router inferenceservice.Router
}

// Infer resolves a build strategy without executing a rebuild, proxying the
// request to the inference services.
// NOTE: This exists only as a way of exposing the inference service while
// limiting the number of services configured to be directly invoked.
func Infer(ctx context.Context, req schema.InferenceRequest, deps *InferDeps) (*schema.StrategyOneOf, error) {
	return deps.Router.Infer(ctx, req, schema.UnspecifiedSize)
}
