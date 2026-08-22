// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshotservice

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDeltaUnconfigured(t *testing.T) {
	_, err := Delta(context.Background(), DeltaRequest{}, &DeltaDeps{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code = %s; want FailedPrecondition. err=%v", status.Code(err), err)
	}
}

func TestDeltaWritesSegment(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{attempts: []schema.RebuildAttempt{
		{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Artifact: "1.0.whl", RunID: "r1", Status: schema.RebuildStatusRunning, Updated: time.Now().UTC()},
	}}
	deps := &DeltaDeps{Source: src, Dest: memfs.New()}
	resp, err := Delta(ctx, DeltaRequest{}, deps)
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}
	if resp.Segment == "" || resp.RowCounts[snapshot.TableAttempts] != 1 {
		t.Errorf("response = %+v, want a segment with 1 attempt", resp)
	}
}
