// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"sort"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/internal/versionx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// VersionState is the coarse status a version's attempts and agent sessions roll
// up to. Detail (which failure phase, how many attempts, etc.) lives in the
// per-version drill-down.
type VersionState string

const (
	StateUntried  VersionState = "untried"
	StateVerified VersionState = "verified"
	StateFailed   VersionState = "failed"
	StateRunning  VersionState = "running"
)

// AgentState summarizes agent activity on a version.
type AgentState string

const (
	AgentNone      AgentState = ""
	AgentRunning   AgentState = "running"
	AgentFixed     AgentState = "fixed"
	AgentFailed    AgentState = "failed"
	AgentThrottled AgentState = "throttled"
)

// VersionStatus is the aggregated status of a single package version across all
// of its rebuild attempts and agent sessions.
type VersionStatus struct {
	Version   string
	Encoded   rebuild.EncodedTarget
	State     VersionState
	Agent     AgentState
	Regressed bool // Failed while an older version is Verified.
	Attempts  int
	Sessions  int
}

// PackageSummary is the high-level status shown at the top of the package view.
type PackageSummary struct {
	Supported      bool // whether the version history was enumerable for this ecosystem
	TotalVersions  int
	Attempted      int
	Verified       int
	Failed         int
	Running        int
	AgentRuns      int
	NeedsAttention int // regressions
	VerifiedPct    int
}

// isVerified reports whether an attempt reproduced upstream.
func isVerified(rb rundex.Rebuild) bool {
	return rb.Success
}

// computeVersionStatuses aggregates rebuilds + agent sessions into a per-version
// status list. When `supported`, `axis` is every published version (newest-first)
// and untried versions are included. Otherwise the list is derived from versions
// we've actually touched, ordered by most-recent activity.
// It is pure (no I/O) so it can be unit tested with canned data.
func computeVersionStatuses(eco rebuild.Ecosystem, pkg string, axis []string, supported bool, rebuilds []rundex.Rebuild, sessions []schema.AgentSession) []VersionStatus {
	type bucket struct {
		attempts []rundex.Rebuild
		sessions []schema.AgentSession
		last     time.Time
	}
	byVer := map[string]*bucket{}
	touch := func(v string, t time.Time) *bucket {
		b := byVer[v]
		if b == nil {
			b = &bucket{}
			byVer[v] = b
		}
		if t.After(b.last) {
			b.last = t
		}
		return b
	}
	for _, rb := range rebuilds {
		b := touch(rb.Version, rb.Created)
		b.attempts = append(b.attempts, rb)
	}
	for i := range sessions {
		s := sessions[i]
		b := touch(s.Target.Version, s.Created)
		b.sessions = append(b.sessions, s)
	}

	var order []string
	if supported {
		order = append(order, axis...)
		seen := make(map[string]bool, len(axis))
		for _, v := range axis {
			seen[v] = true
		}
		// Include any attempted version missing from the published axis (e.g. a
		// pre-release we tried) so its data isn't dropped.
		var extra []string
		for v := range byVer {
			if !seen[v] {
				extra = append(extra, v)
			}
		}
		sort.Slice(extra, func(i, j int) bool { return byVer[extra[i]].last.After(byVer[extra[j]].last) })
		order = append(order, extra...)
	} else {
		for v := range byVer {
			order = append(order, v)
		}
		// Newest first, matching the published-axis ordering.
		sort.Slice(order, func(i, j int) bool { return versionx.ApproxCompare(order[i], order[j]) > 0 })
	}

	out := make([]VersionStatus, 0, len(order))
	for _, v := range order {
		vs := VersionStatus{
			Version: v,
			Encoded: packagePathEncoding.Encode(rebuild.Target{Ecosystem: eco, Package: pkg, Version: v}),
			State:   StateUntried,
		}
		if b := byVer[v]; b != nil {
			vs.Attempts = len(b.attempts)
			vs.Sessions = len(b.sessions)
			classifyVersion(&vs, b.attempts, b.sessions)
		}
		out = append(out, vs)
	}
	// Regression only makes sense on a semver-ordered axis.
	if supported {
		markRegressions(out)
	}
	return out
}

// classifyVersion sets State/Agent from a version's attempts+sessions.
func classifyVersion(vs *VersionStatus, attempts []rundex.Rebuild, sessions []schema.AgentSession) {
	var verified, running, failed bool
	for _, a := range attempts {
		if isVerified(a) {
			verified = true
		}
		if a.Status == schema.RebuildStatusRunning {
			running = true
		}
		if !isVerified(a) && a.Status != schema.RebuildStatusRunning {
			failed = true
		}
	}
	agent := AgentNone
	for _, s := range sessions {
		switch {
		case s.Status == schema.AgentSessionStatusRunning || s.Status == schema.AgentSessionStatusInitializing:
			running = true
			if agent != AgentFixed {
				agent = AgentRunning
			}
		case s.StopReason == schema.AgentCompleteReasonSuccess:
			verified = true
			agent = AgentFixed
		case s.StopReason == schema.AgentCompleteReasonFailed || s.StopReason == schema.AgentCompleteReasonError:
			failed = true
			if agent == AgentNone {
				agent = AgentFailed
			}
		case s.StopReason == schema.AgentCompleteReasonThrottled:
			if agent == AgentNone {
				agent = AgentThrottled
			}
		}
	}
	vs.Agent = agent
	switch {
	case verified:
		vs.State = StateVerified
	case running:
		vs.State = StateRunning
	case failed:
		vs.State = StateFailed
	default:
		vs.State = StateUntried
	}
}

// markRegressions flags Failed versions that have a Verified *older* version
// (list is newest-first).
func markRegressions(vs []VersionStatus) {
	sawVerifiedOlder := false
	for i := len(vs) - 1; i >= 0; i-- {
		switch vs[i].State {
		case StateVerified:
			sawVerifiedOlder = true
		case StateFailed:
			if sawVerifiedOlder {
				vs[i].Regressed = true
			}
		}
	}
}

// summarize rolls up a version-status list into the package header summary.
func summarize(statuses []VersionStatus, supported bool) PackageSummary {
	s := PackageSummary{Supported: supported, TotalVersions: len(statuses)}
	for _, v := range statuses {
		if v.Attempts > 0 || v.Sessions > 0 {
			s.Attempted++
		}
		if v.Sessions > 0 {
			s.AgentRuns += v.Sessions
		}
		switch v.State {
		case StateVerified:
			s.Verified++
		case StateFailed:
			s.Failed++
		case StateRunning:
			s.Running++
		}
		if v.Regressed {
			s.NeedsAttention++
		}
	}
	denom := s.Attempted
	if supported {
		denom = s.TotalVersions
	}
	if denom > 0 {
		s.VerifiedPct = int(100 * s.Verified / denom)
	}
	return s
}
