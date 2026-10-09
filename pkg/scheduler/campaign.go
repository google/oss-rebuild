// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"math"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// Stage is a rung on the escalation ladder: one automated way of attempting a
// rebuild, each more expensive than the last. Escalation past the cheap stages
// is what the budget rations.
type Stage string

const (
	// StageReplay re-runs a known or promoted strategy with no inference.
	// Reserved for sibling fan-out. Nothing enqueues at it yet: every
	// campaign starts at StageInfer.
	StageReplay Stage = "replay"
	// StageInfer runs heuristic inference plus a build.
	StageInfer Stage = "infer"
	// StageAgent runs the multi-iteration LLM agent. Rationed.
	StageAgent Stage = "agent"
)

// stages orders the ladder, cheapest first.
var stages = []Stage{StageReplay, StageInfer, StageAgent}

// Next returns the stage after s. The second return is false when s is the
// last (or an unknown) stage, meaning nothing more expensive is left to try.
func (s Stage) Next() (Stage, bool) {
	for i, cur := range stages[:len(stages)-1] {
		if cur == s {
			return stages[i+1], true
		}
	}
	return "", false
}

// Outcome is the classification of a single attempt. Exactly one applies.
type Outcome string

const (
	OutcomePending   Outcome = ""          // no terminal result yet
	OutcomeAttested  Outcome = "ATTESTED"  // reproduced, an attestation exists
	OutcomeTransient Outcome = "TRANSIENT" // throttle or infra flake, retry same stage
	OutcomeFailure   Outcome = "FAILURE"   // ran and failed, escalate
)

// State tracks where a campaign sits in the dispatch workflow.
type State string

const (
	StateQueued      State = "QUEUED"       // eligible for dispatch at Stage
	StateInFlight    State = "INFLIGHT"     // dispatched, awaiting outcome
	StateNeedsTriage State = "NEEDS_TRIAGE" // automated stages exhausted, awaiting a human
	StateDone        State = "DONE"         // attested
)

// Campaign is the queue-state document for one package version working
// through the escalation ladder. Document ID is TargetID(Target()).
type Campaign struct {
	Ecosystem string `firestore:"ecosystem,omitempty"`
	Package   string `firestore:"package,omitempty"`
	Version   string `firestore:"version,omitempty"`
	Artifact  string `firestore:"artifact,omitempty"`

	Stage    Stage   `firestore:"stage,omitempty"` // next stage to run
	State    State   `firestore:"state,omitempty"`
	Outcome  Outcome `firestore:"outcome,omitempty"`
	Attempts int     `firestore:"attempts,omitempty"`
	Retries  int     `firestore:"retries,omitempty"` // same-stage transient retries

	LastRunID    string `firestore:"last_run_id,omitempty"`
	LastSession  string `firestore:"last_session,omitempty"`
	Repo         string `firestore:"repo,omitempty"` // discovered source repo (for jumbo routing)
	TriageReason string `firestore:"triage_reason,omitempty"`

	// Score is the package's priority specialized to this version, copied at
	// enqueue. Recency is deliberately not stored: DispatchOrder derives it
	// from Published at read time, so the boost decays while the campaign
	// waits instead of freezing at whatever it was when it was enqueued.
	Score     float64   `firestore:"score,omitempty"`
	Published time.Time `firestore:"published,omitempty"`

	DispatchedAt time.Time `firestore:"dispatched_at,omitempty"`
	Updated      time.Time `firestore:"updated,omitempty"`
}

// DispatchOrder is the queue position of a campaign as of now, highest first.
// Importance and recency multiply rather than add so that a stale version of a
// critical package and a fresh version of an unimportant one both stay ranked
// below a fresh version of a critical one.
func (c Campaign) DispatchOrder(now time.Time, k, tauHours float64) float64 {
	return c.Score * Freshness(c.Published, now, k, tauHours)
}

func (c Campaign) Target() rebuild.Target {
	return rebuild.Target{
		Ecosystem: rebuild.Ecosystem(c.Ecosystem),
		Package:   c.Package,
		Version:   c.Version,
		Artifact:  c.Artifact,
	}
}

// Default freshness parameters, shared by enqueue-time admission and
// dispatch-time ordering so the two rank alike.
const (
	DefaultFreshnessK        = 3
	DefaultFreshnessTauHours = 120
)

// Freshness returns the recency boost for a campaign: 1 + k*exp(-age/tau), so
// new releases spike and decay into the backlog. tauHours must be > 0.
func Freshness(published, now time.Time, k, tauHours float64) float64 {
	if published.IsZero() || tauHours <= 0 {
		return 1
	}
	age := now.Sub(published).Hours()
	if age < 0 {
		age = 0
	}
	return 1 + k*math.Exp(-age/tauHours)
}
