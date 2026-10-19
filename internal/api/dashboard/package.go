// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/pkg/errors"
)

var _ api.HandlerFn[PackageRequest, PackageData, *Deps] = Package

type PackageRequest struct {
	Ecosystem string
	Package   string
}

func (PackageRequest) Validate() error { return nil }

type PackageData struct {
	Frame
	Ecosystem   string
	PackageName string
	Upstream    string          // the repo of the latest attempt whose strategy names one
	Summary     PackageSummary  // the figures above the ledger
	Versions    []VersionStatus // the attempted versions, newest first
	Untried     int             // published versions never attempted
	Causes      []FailureCause  // the most common first
	MoreCauses  int             // causes left out of Causes
	Warning     string          // set when the version lookup failed, not when the ecosystem lacks a lister
}

// UpstreamLabel is Upstream without its scheme.
func (d PackageData) UpstreamLabel() string {
	if _, rest, ok := strings.Cut(d.Upstream, "://"); ok {
		return rest
	}
	return d.Upstream
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

	// The published versions only count the untried ones. An ecosystem that
	// can't enumerate them is expected, but a failed lookup is worth saying.
	var warning string
	published, err := publishedVersions(ctx, deps.Registry, rebuild.Target{Ecosystem: eco, Package: req.Package})
	if err != nil && !errors.Is(err, errNoVersionLister) {
		log.Printf("Listing versions for %s %s: %v", eco, req.Package, err)
		warning = "Version listing failed, so versions never attempted are not counted."
	}

	events := make([]PackageEvent, 0, len(rebuilds)+len(sessions))
	var upstream string
	var upstreamAt time.Time
	for _, rb := range rebuilds {
		events = append(events, rebuildEvent(rb))
		if repo := scheduler.RepoFromStrategy(rb.Strategy); repo != "" && !rb.Created.Before(upstreamAt) {
			upstream, upstreamAt = repo, rb.Created
		}
	}
	for _, s := range sessions {
		events = append(events, sessionEvent(s))
	}
	statuses, untried := computeVersionStatuses(eco, req.Package, published, events)
	causes, more := failureCauses(statuses)
	return &PackageData{
		Ecosystem:   req.Ecosystem,
		PackageName: req.Package,
		Upstream:    upstream,
		Summary:     summarize(statuses),
		Versions:    statuses,
		Untried:     untried,
		Causes:      causes,
		MoreCauses:  more,
		Warning:     warning,
	}, nil
}
