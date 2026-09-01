// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/docdb"
	"github.com/pkg/errors"
)

// DeltaOptions configures a delta run.
type DeltaOptions struct {
	// Now overrides the segment timestamp. Zero means time.Now().UTC().
	Now time.Time
	// Slack widens the resume window to capture clock skew with servers and
	// writes with stamps that may be slow to appear in the database.
	// The guarded upserts make the resulting overlap harmless. Zero means 2m.
	Slack time.Duration
	// Bootstrap is how far back the first delta (no prior segments)
	// reaches. Anything older is covered by the snapshot rollup. Zero
	// means 24h.
	Bootstrap time.Duration
}

// DeltaResult reports what a delta run wrote.
type DeltaResult struct {
	// Since is the watermark the source was queried from.
	Since time.Time
	// Segment is the written object name, empty when nothing had changed.
	Segment   string
	RowCounts map[string]int
}

// Delta writes one segment of everything written since the previous
// segment. It is stateless: the resume watermark is recovered from the
// newest segment name at the destination, so a crashed or skipped run is
// caught up by the next one.
func Delta(ctx context.Context, src Source, dest billy.Filesystem, opts DeltaOptions) (*DeltaResult, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	slack := opts.Slack
	if slack == 0 {
		slack = 2 * time.Minute
	}
	bootstrap := opts.Bootstrap
	if bootstrap == 0 {
		bootstrap = 24 * time.Hour
	}
	segs, err := docdb.ListSegments(dest, DeltaPrefix, "")
	if err != nil {
		return nil, errors.Wrap(err, "listing segments")
	}
	since := now.Add(-bootstrap)
	if len(segs) > 0 {
		t, err := docdb.SegmentTime(segs[len(segs)-1])
		if err != nil {
			return nil, err
		}
		since = t.Add(-slack)
	}
	attempts, err := src.Attempts(ctx, since)
	if err != nil {
		return nil, errors.Wrap(err, "scanning attempts")
	}
	runs, err := src.Runs(ctx, since)
	if err != nil {
		return nil, errors.Wrap(err, "scanning runs")
	}
	sessions, err := src.Sessions(ctx, since)
	if err != nil {
		return nil, errors.Wrap(err, "scanning agent sessions")
	}
	iterations, err := src.Iterations(ctx, since)
	if err != nil {
		return nil, errors.Wrap(err, "scanning agent iterations")
	}
	scratches, err := src.Scratches(ctx, since)
	if err != nil {
		return nil, errors.Wrap(err, "scanning scratch VMs")
	}
	execs, err := src.Execs(ctx, since)
	if err != nil {
		return nil, errors.Wrap(err, "scanning scratch execs")
	}
	repoMetrics, err := src.RepoMetrics(ctx, since)
	if err != nil {
		return nil, errors.Wrap(err, "scanning repo metrics")
	}
	tables := map[string][]json.RawMessage{
		TableAttempts:        docsOf(attempts),
		TableRuns:            docsOf(runs),
		TableAgentSessions:   docsOf(sessions),
		TableAgentIterations: docsOf(iterations),
		TableScratchVMs:      docsOf(scratches),
		TableScratchExecs:    docsOf(execs),
		TableRepoMetrics:     docsOf(repoMetrics),
	}
	res := &DeltaResult{Since: since, RowCounts: make(map[string]int, len(tables))}
	for name, docs := range tables {
		res.RowCounts[name] = len(docs)
	}
	res.Segment, err = docdb.WriteSegment(dest, DeltaPrefix, now, Tables(), tables)
	if err != nil {
		return nil, errors.Wrap(err, "writing segment")
	}
	return res, nil
}
