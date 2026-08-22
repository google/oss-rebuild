// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

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

// DeltaRequest is the (empty) input to /snapshot/delta.
type DeltaRequest struct{}

// Validate implements act.Input.
func (DeltaRequest) Validate() error { return nil }

// DeltaResponse reports the delta segment that was written. Segment is
// empty when nothing had changed since the previous delta.
type DeltaResponse struct {
	Since     time.Time      `json:"since"`
	Segment   string         `json:"segment,omitempty"`
	RowCounts map[string]int `json:"row_counts"`
}

// DeltaDeps wires the delta handler.
type DeltaDeps struct {
	Source snapshot.Source  // records written since the resume point
	Dest   billy.Filesystem // segment destination, also listed for the resume point. when nil, endpoint reports error
	Opts   snapshot.DeltaOptions
}

// Delta writes one delta segment to the configured analytics
// destination. It is stateless: the resume point is recovered from the
// newest segment at the destination, so it is safe to invoke repeatedly and
// a skipped run is caught up by the next one.
func Delta(ctx context.Context, _ DeltaRequest, deps *DeltaDeps) (*DeltaResponse, error) {
	if deps.Dest == nil {
		return nil, api.AsStatus(codes.FailedPrecondition, errors.New("the delta export is disabled: no destination configured"))
	}
	res, err := snapshot.Delta(ctx, deps.Source, deps.Dest, deps.Opts)
	if err != nil {
		return nil, api.AsStatus(codes.Internal, errors.Wrap(err, "running delta"))
	}
	return &DeltaResponse{Since: res.Since, Segment: res.Segment, RowCounts: res.RowCounts}, nil
}
