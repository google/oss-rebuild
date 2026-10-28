// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestSelect(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	campaign := func(pkg string, stage Stage, score float64, age time.Duration) Campaign {
		return Campaign{Ecosystem: "npm", Package: pkg, Version: "1.0.0", Stage: stage, State: StateQueued, Score: score, Published: now.Add(-age)}
	}
	for _, tc := range []struct {
		name   string
		queued []Campaign
		caps   Caps
		k      float64
		want   []string
	}{
		{
			name:   "HighestOrderFirst",
			queued: []Campaign{campaign("low", StageInfer, 0.1, 0), campaign("high", StageInfer, 0.9, 0)},
			want:   []string{"high", "low"},
		},
		{
			// A fresh release of a mid-tier package outranks a stale version
			// of a critical one.
			name:   "FreshnessBoosts",
			queued: []Campaign{campaign("stale-critical", StageInfer, 0.9, 30*24*time.Hour), campaign("fresh-mid", StageInfer, 0.5, 0)},
			k:      DefaultFreshnessK,
			want:   []string{"fresh-mid", "stale-critical"},
		},
		{
			name:   "TiesNewestFirst",
			queued: []Campaign{campaign("old", StageInfer, 0.5, 48*time.Hour), campaign("new", StageInfer, 0.5, time.Hour)},
			want:   []string{"new", "old"},
		},
		{
			name:   "BatchCap",
			queued: []Campaign{campaign("a", StageInfer, 0.3, 0), campaign("b", StageInfer, 0.2, 0), campaign("c", StageInfer, 0.1, 0)},
			caps:   Caps{Batch: 2},
			want:   []string{"a", "b"},
		},
		{
			// The agent cap must not starve the cheaper stages queued behind
			// the agent campaigns it holds back.
			name:   "AgentCapPassesOver",
			queued: []Campaign{campaign("a1", StageAgent, 0.9, 0), campaign("a2", StageAgent, 0.8, 0), campaign("i1", StageInfer, 0.1, 0)},
			caps:   Caps{Agent: 1},
			want:   []string{"a1", "i1"},
		},
		{
			name:   "BuildCapPassesOver",
			queued: []Campaign{campaign("i1", StageInfer, 0.9, 0), campaign("i2", StageInfer, 0.8, 0), campaign("a1", StageAgent, 0.1, 0)},
			caps:   Caps{Build: 1},
			want:   []string{"i1", "a1"},
		},
		{
			// A held campaign is passed over until its time, not dropped.
			name: "NotBeforeHolds",
			queued: []Campaign{
				{Ecosystem: "npm", Package: "held", Version: "1.0.0", Stage: StageInfer, Score: 0.9, NotBefore: now.Add(time.Minute)},
				{Ecosystem: "npm", Package: "settled", Version: "1.0.0", Stage: StageInfer, Score: 0.1, NotBefore: now.Add(-time.Minute)},
			},
			want: []string{"settled"},
		},
		{
			name:   "CapNoneAdmitsNone",
			queued: []Campaign{campaign("a1", StageAgent, 0.9, 0), campaign("i1", StageInfer, 0.1, 0)},
			caps:   Caps{Agent: CapNone},
			want:   []string{"i1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, c := range Select(tc.queued, tc.caps, now, tc.k, DefaultFreshnessTauHours) {
				got = append(got, c.Package)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Select mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
