// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/oss-rebuild/internal/docdb"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestDeltaBootstrapAndResume(t *testing.T) {
	ctx := context.Background()
	dest := memfs.New()
	now := time.Date(2026, 8, 20, 14, 5, 0, 0, time.UTC)
	src := &fakeSource{
		attempts: []schema.RebuildAttempt{
			{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Artifact: "a.whl", RunID: "r1", Status: schema.RebuildStatusRunning, Updated: now},
		},
		scratches: []schema.Scratch{
			{ID: "sc1", State: schema.ScratchReady, Created: now.Add(-time.Hour), Updated: now},
		},
	}
	// With no prior segments the bootstrap lookback applies.
	res, err := Delta(ctx, src, dest, DeltaOptions{Now: now})
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}
	if want := now.Add(-24 * time.Hour); !res.Since.Equal(want) {
		t.Errorf("bootstrap since = %v, want %v", res.Since, want)
	}
	if res.Segment == "" || res.RowCounts[TableAttempts] != 1 || res.RowCounts[TableScratchVMs] != 1 {
		t.Errorf("first delta = %+v, want a segment with 1 attempt and 1 scratch", res)
	}
	// A later run resumes from the newest segment minus the slack window.
	res2, err := Delta(ctx, src, dest, DeltaOptions{Now: now.Add(5 * time.Minute)})
	if err != nil {
		t.Fatalf("second Delta: %v", err)
	}
	if want := now.Add(-2 * time.Minute); !res2.Since.Equal(want) {
		t.Errorf("resume since = %v, want %v", res2.Since, want)
	}
	if res2.Segment == "" {
		t.Error("second delta wrote no segment despite changed rows")
	}
	// A quiet tick writes nothing, and the resume point holds position.
	quiet := &fakeSource{}
	res3, err := Delta(ctx, quiet, dest, DeltaOptions{Now: now.Add(10 * time.Minute)})
	if err != nil {
		t.Fatalf("third Delta: %v", err)
	}
	if res3.Segment != "" {
		t.Errorf("quiet delta wrote %q", res3.Segment)
	}
	if want := now.Add(5*time.Minute - 2*time.Minute); !res3.Since.Equal(want) {
		t.Errorf("quiet since = %v, want %v", res3.Since, want)
	}
	segs, err := docdb.ListSegments(dest, DeltaPrefix, "")
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(segs) != 2 {
		t.Errorf("segments = %v, want 2", segs)
	}
}
