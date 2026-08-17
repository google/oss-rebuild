// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestTick(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		in         Campaign
		outcome    Outcome
		maxRetries int
		want       Campaign
	}{
		{
			name:       "AttestedIsDone",
			in:         Campaign{Stage: StageInfer, State: StateInFlight},
			outcome:    OutcomeAttested,
			maxRetries: 3,
			want:       Campaign{Stage: StageInfer, State: StateDone, Outcome: OutcomeAttested, Updated: now},
		},
		{
			// Retries are per stage, so escalation starts a fresh budget.
			name:       "InferFailureEscalatesToAgent",
			in:         Campaign{Stage: StageInfer, State: StateInFlight, Retries: 2},
			outcome:    OutcomeFailure,
			maxRetries: 3,
			want:       Campaign{Stage: StageAgent, State: StateQueued, Outcome: OutcomeFailure, Updated: now},
		},
		{
			// Nothing more expensive exists above the agent stage.
			name:       "AgentFailureNeedsTriage",
			in:         Campaign{Stage: StageAgent, State: StateInFlight},
			outcome:    OutcomeFailure,
			maxRetries: 3,
			want: Campaign{Stage: StageAgent, State: StateNeedsTriage, Outcome: OutcomeFailure,
				TriageReason: "agent stage exhausted", Updated: now},
		},
		{
			// Transient failures say nothing about the package, so they must
			// not buy it a more expensive stage.
			name:       "TransientRetriesSameStage",
			in:         Campaign{Stage: StageAgent, State: StateInFlight},
			outcome:    OutcomeTransient,
			maxRetries: 3,
			want:       Campaign{Stage: StageAgent, State: StateQueued, Outcome: OutcomeTransient, Retries: 1, Updated: now},
		},
		{
			name:       "TransientNeedsTriageAfterMaxRetries",
			in:         Campaign{Stage: StageAgent, State: StateInFlight, Retries: 2},
			outcome:    OutcomeTransient,
			maxRetries: 3,
			want: Campaign{Stage: StageAgent, State: StateNeedsTriage, Outcome: OutcomeTransient,
				Retries: 3, TriageReason: "persistent transient failures", Updated: now},
		},
		{
			name:       "UnboundedRetriesNeverStop",
			in:         Campaign{Stage: StageAgent, State: StateInFlight, Retries: 99},
			outcome:    OutcomeTransient,
			maxRetries: 0,
			want:       Campaign{Stage: StageAgent, State: StateQueued, Outcome: OutcomeTransient, Retries: 100, Updated: now},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Tick(tc.in, tc.outcome, tc.maxRetries, now)); diff != "" {
				t.Errorf("Tick mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
