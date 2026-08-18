// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"slices"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

var _ api.HandlerFn[PackageRequest, PackageData, *Deps] = Package

// The status strip is a grid of stripCols columns. Collapsed it shows one row.
// Expanded it shows a bounded matrix of stripExpandedRows rows. The page size
// (and thus how far older/newer paginate) follows the current mode. stripCols
// must match the grid-template-columns count in dashboard.css.
const (
	stripCols         = 50
	stripExpandedRows = 4
)

// eventsWindow caps how many recent events the timeline renders.
const eventsWindow = 100

type PackageRequest struct {
	Ecosystem string
	Package   string
	Offset    int  // render strip scroll-back into the version history, 0 = newest
	Expanded  bool // render strip as a multi-row grid rather than a single row
}

func (PackageRequest) Validate() error { return nil }

// PackageEvent is a single entry in a package's chronological history: either a
// rebuild attempt or an agent session. Exactly one of Rebuild/Session is set.
type PackageEvent struct {
	Created time.Time
	Rebuild *RebuildView
	Session *schema.AgentSession
}

type PackageData struct {
	Ecosystem      string
	PackageName    string
	EncodedPackage string
	Summary        PackageSummary  // high-level per-package status shown at the top
	Versions       []VersionStatus // windowed slice of the version history shown in the strip
	Expanded       bool            // strip shown as a multi-row grid
	Offset         int             // current window offset (for building toggle links)
	HasPrev        bool            // a newer window exists (scroll toward newest)
	HasNext        bool            // an older window exists (scroll back in time)
	PrevOffset     int             // offset the newer-window link jumps to
	NextOffset     int             // offset the older-window link jumps to
	WindowFrom     int             // 1-based index of the first shown version (for the caption)
	WindowTo       int             // 1-based index of the last shown version
	Events         []PackageEvent  // rebuild attempts and agent sessions, most-recent-first
}

func Package(ctx context.Context, req PackageRequest, deps *Deps) (*PackageData, error) {
	eco := rebuild.Ecosystem(req.Ecosystem)

	// Fetch attempts across all versions of the package, include in-flight.
	rebuilds, err := deps.Rundex.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{
		Target: &rebuild.Target{Ecosystem: eco, Package: req.Package},
		Opts:   rundex.FetchRebuildOpts{IncludePending: true},
		Limit:  1000,
	})
	if err != nil {
		return nil, errors.Wrap(err, "fetching rebuilds")
	}

	applySuccessRegex(deps.SuccessRegex, rebuilds)

	// Fetch agent sessions for this package. Sessions are supplementary, so a nil
	// reader (e.g. in tests) simply yields none.
	var sessions []schema.AgentSession
	if deps.Sessions != nil {
		sessions, err = deps.Sessions.FetchSessions(ctx, &rundex.FetchSessionsReq{
			PartialTarget: &rebuild.Target{Ecosystem: eco, Package: req.Package},
		})
		if err != nil {
			return nil, errors.Wrap(err, "fetching agent sessions")
		}
	}

	statuses := computeVersionStatuses(eco, req.Package, rebuilds, sessions)
	summary := summarize(statuses)

	// Window the version history for the strip.
	pageSize := stripCols
	if req.Expanded {
		pageSize = stripCols * stripExpandedRows
	}
	total := len(statuses)
	off := req.Offset
	if off < 0 || off >= total {
		off = 0
	}
	end := min(off+pageSize, total)
	window := statuses[off:end]

	// Intermingle rebuilds and sessions into a single timeline, most recent
	// first, capped to the events window.
	events := make([]PackageEvent, 0, len(rebuilds)+len(sessions))
	for _, rb := range rebuilds {
		view := NewRebuildView(rb)
		events = append(events, PackageEvent{Created: rb.Created, Rebuild: &view})
	}
	for i := range sessions {
		events = append(events, PackageEvent{Created: sessions[i].Created, Session: &sessions[i]})
	}
	slices.SortFunc(events, func(a, b PackageEvent) int {
		return b.Created.Compare(a.Created)
	})
	if len(events) > eventsWindow {
		events = events[:eventsWindow]
	}

	et := packagePathEncoding.Encode(rebuild.Target{Ecosystem: eco, Package: req.Package})

	data := &PackageData{
		Ecosystem:      req.Ecosystem,
		PackageName:    req.Package,
		EncodedPackage: et.Package,
		Summary:        summary,
		Versions:       window,
		Expanded:       req.Expanded,
		Offset:         off,
		Events:         events,
	}
	if total > 0 {
		data.WindowFrom = off + 1
		data.WindowTo = end
	}
	if off > 0 {
		data.HasPrev = true
		data.PrevOffset = max(0, off-pageSize)
	}
	if end < total {
		data.HasNext = true
		data.NextOffset = end
	}
	return data, nil
}
