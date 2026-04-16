// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type CreateRunDeps struct {
	Runs db.Runs
}

func CreateRun(ctx context.Context, req schema.CreateRunRequest, deps *CreateRunDeps) (*schema.Run, error) {
	run := schema.Run{
		ID:            time.Now().UTC().Format(time.RFC3339),
		BenchmarkName: req.BenchmarkName,
		BenchmarkHash: req.BenchmarkHash,
		Type:          req.Type,
		Created:       time.Now().UTC(),
	}
	if err := deps.Runs.Insert(ctx, run); err != nil {
		code := codes.Internal
		if errors.Is(err, db.ErrAlreadyExists) {
			code = codes.AlreadyExists
		}
		return nil, api.AsStatus(code, errors.Wrap(err, "db write"))
	}
	return &run, nil
}
