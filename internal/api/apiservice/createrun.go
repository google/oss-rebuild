// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package apiservice

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

type CreateRunDeps struct {
	FirestoreClient *firestore.Client
}

func CreateRun(ctx context.Context, req schema.CreateRunRequest, deps *CreateRunDeps) (*schema.Run, error) {
	run := schema.Run{
		ID:            time.Now().UTC().Format(time.RFC3339),
		BenchmarkName: req.BenchmarkName,
		BenchmarkHash: req.BenchmarkHash,
		Type:          req.Type,
	}
	err := deps.FirestoreClient.RunTransaction(ctx, func(ctx context.Context, t *firestore.Transaction) error {
		run.Created = time.Now().UTC()
		return t.Create(deps.FirestoreClient.Collection("runs").Doc(run.ID), run)
	})
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "firestore write"))
	}
	return &run, nil
}
