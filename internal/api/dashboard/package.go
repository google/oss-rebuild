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

type PackageRequest struct {
	Ecosystem string
	Package   string
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
	Events         []PackageEvent // rebuild attempsts and agent sessions, most-recent-first
}

func Package(ctx context.Context, req PackageRequest, deps *Deps) (*PackageData, error) {
	// Fetch rebuild attempts for this specific package.
	rebuilds, err := deps.Rundex.RecentPackageRebuilds(ctx, rebuild.Ecosystem(req.Ecosystem), req.Package)
	if err != nil {
		return nil, errors.Wrap(err, "fetching rebuilds")
	}

	applySuccessRegex(deps.SuccessRegex, rebuilds)

	// Fetch agent sessions for this package. Sessions are supplementary, so a nil
	// reader (e.g. in tests) simply yields none.
	var sessions []schema.AgentSession
	if deps.Sessions != nil {
		sessions, err = deps.Sessions.FetchSessions(ctx, &rundex.FetchSessionsReq{
			PartialTarget: &rebuild.Target{
				Ecosystem: rebuild.Ecosystem(req.Ecosystem),
				Package:   req.Package,
			},
		})
		if err != nil {
			return nil, errors.Wrap(err, "fetching agent sessions")
		}
	}

	// Intermingle rebuilds and sessions into a single timeline, most recent first.
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

	et := packagePathEncoding.Encode(rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
	})

	return &PackageData{
		Ecosystem:      req.Ecosystem,
		PackageName:    req.Package,
		EncodedPackage: et.Package,
		Events:         events,
	}, nil
}
