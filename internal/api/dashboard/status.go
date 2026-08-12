// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"sort"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
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
	AgentNone    AgentState = ""
	AgentRunning AgentState = "running"
	AgentFixed   AgentState = "fixed"
	AgentFailed  AgentState = "failed"
)

// VersionStatus is the aggregated status of a single package version across all
// of its rebuild attempts and agent sessions.
type VersionStatus struct {
	Version  string
	Encoded  rebuild.EncodedTarget
	State    VersionState
	Agent    AgentState
	Attempts int
	Sessions int
}

// PackageSummary is the high-level status shown at the top of the package view.
type PackageSummary struct {
	Attempted   int // versions with at least one attempt or session
	Verified    int
	Failed      int
	Running     int
	AgentRuns   int
	VerifiedPct int
}

// isVerified reports whether an attempt reproduced upstream.
func isVerified(rb rundex.Rebuild) bool {
	return rb.Success
}

// computeVersionStatuses aggregates rebuilds + agent sessions into a per-version
// status list covering the versions we've touched, most recently active first.
// It is pure (no I/O) so it can be unit tested with canned data.
func computeVersionStatuses(eco rebuild.Ecosystem, pkg string, rebuilds []rundex.Rebuild, sessions []schema.AgentSession) []VersionStatus {
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
	for v := range byVer {
		order = append(order, v)
	}
	sort.Slice(order, func(i, j int) bool { return byVer[order[i]].last.After(byVer[order[j]].last) })

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

// summarize rolls up a version-status list into the package header summary.
func summarize(statuses []VersionStatus) PackageSummary {
	var s PackageSummary
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
	}
	if s.Attempted > 0 {
		s.VerifiedPct = int(100 * s.Verified / s.Attempted)
	}
	return s
}
