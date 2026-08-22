// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package snapshotservice exposes the internal/snapshot pipelines.
// /snapshot/rollup rebuilds the published database while recurring updates are
// handled by /snapshot/delta. These are idempotent and, thus, safe to invoke
// via a naive scheduler.
package snapshotservice

import (
	"context"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
)

// RollupRequest is the (empty) input to /snapshot/rollup.
type RollupRequest struct{}

// Validate implements act.Input.
func (RollupRequest) Validate() error { return nil }

// RollupResponse reports the snapshot that was written.
type RollupResponse struct {
	SnapshotTime  time.Time      `json:"snapshot_time"`
	Watermark     time.Time      `json:"watermark"`
	SchemaVersion int            `json:"schema_version"`
	RowCounts     map[string]int `json:"row_counts"`
}

// RollupDeps wires the rollup handler.
type RollupDeps struct {
	Source snapshot.Source  // raw records comprising the snapshot
	Dest   billy.Filesystem // snapshot destination. when nil, endpoint reports error
	Opts   snapshot.Options
}

// Rollup scans the source and writes the snapshot database to the configured
// analytics destination, replacing the published object wholesale.
func Rollup(ctx context.Context, _ RollupRequest, deps *RollupDeps) (*RollupResponse, error) {
	if deps.Dest == nil || deps.Source == nil {
		return nil, api.AsStatus(codes.FailedPrecondition, errors.New("the rollup is disabled: no destination configured"))
	}
	res, err := snapshot.Rollup(ctx, deps.Source, deps.Dest, deps.Opts)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "running rollup"))
	}
	return &RollupResponse{
		SnapshotTime:  res.Meta.BuiltAt,
		Watermark:     res.Meta.Watermark,
		SchemaVersion: snapshot.SchemaVersion,
		RowCounts:     res.RowCounts,
	}, nil
}
