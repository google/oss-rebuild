// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"slices"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act/api"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

var _ api.HandlerFn[VersionRequest, VersionData, *Deps] = Version

type VersionRequest struct {
	Ecosystem string
	Package   string
	Version   string
}

func (VersionRequest) Validate() error { return nil }

type VersionData struct {
	Ecosystem      string
	PackageName    string
	Version        string
	EncodedPackage string
	// Attempts are this version's rebuild attempts (across artifacts), newest-first.
	Attempts []RebuildView
	// Sessions are this version's agent sessions with their iterations, newest-first.
	Sessions []SessionView
}

// Version aggregates every rebuild attempt and agent session (with iterations)
// for a single package version.
func Version(ctx context.Context, req VersionRequest, deps *Deps) (*VersionData, error) {
	target := rebuild.Target{Ecosystem: rebuild.Ecosystem(req.Ecosystem), Package: req.Package, Version: req.Version}

	rebuilds, err := deps.Rundex.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{
		Target: &target,
		Opts:   rundex.FetchRebuildOpts{IncludePending: true},
		Limit:  200,
	})
	if err != nil {
		return nil, errors.Wrap(err, "fetching attempts")
	}
	slices.SortFunc(rebuilds, func(a, b rundex.Rebuild) int { return b.Created.Compare(a.Created) })
	attempts := make([]RebuildView, 0, len(rebuilds))
	for _, rb := range rebuilds {
		attempts = append(attempts, NewRebuildView(rb))
	}

	var sessions []SessionView
	if deps.Sessions != nil {
		ss, err := deps.Sessions.FetchSessions(ctx, &rundex.FetchSessionsReq{PartialTarget: &target})
		if err != nil {
			return nil, errors.Wrap(err, "fetching agent sessions")
		}
		slices.SortFunc(ss, func(a, b schema.AgentSession) int { return b.Created.Compare(a.Created) })
		for i := range ss {
			// Iterations are best-effort. A failure to load them shouldn't sink the page.
			iters, _ := deps.Sessions.FetchIterations(ctx, &rundex.FetchIterationsReq{SessionID: ss[i].ID})
			slices.SortFunc(iters, func(a, b schema.AgentIteration) int { return a.Number - b.Number })
			view := NewSessionView(ss[i])
			view.Iterations = iters
			sessions = append(sessions, view)
		}
	}

	et := packagePathEncoding.Encode(target)
	return &VersionData{
		Ecosystem:      req.Ecosystem,
		PackageName:    req.Package,
		Version:        req.Version,
		EncodedPackage: et.Package,
		Attempts:       attempts,
		Sessions:       sessions,
	}, nil
}
