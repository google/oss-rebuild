// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestStageNext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     Stage
		want   Stage
		wantOK bool
	}{
		{"ReplayEscalatesToInfer", StageReplay, StageInfer, true},
		{"InferEscalatesToAgent", StageInfer, StageAgent, true},
		{"AgentIsLast", StageAgent, "", false},
		{"UnknownStage", Stage("bogus"), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.in.Next()
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("%q.Next() = (%q, %t), want (%q, %t)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFreshness(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		published time.Time
		tauHours  float64
		want      float64
	}{
		// An unknown publish date must not boost or penalize, so it lands at
		// the multiplicative identity.
		{"UnknownPublishDate", time.Time{}, 120, 1},
		{"NonPositiveTau", now, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Freshness(tc.published, now, 3, tc.tauHours)); diff != "" {
				t.Errorf("Freshness mismatch (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("DecaysWithAge", func(t *testing.T) {
		fresh := Freshness(now.Add(-time.Hour), now, 3, 120)
		old := Freshness(now.Add(-30*24*time.Hour), now, 3, 120)
		if !(fresh > old && old >= 1) {
			t.Errorf("expected fresh (%v) > old (%v) >= 1", fresh, old)
		}
	})
	t.Run("FutureDateDoesNotAmplify", func(t *testing.T) {
		// Registry clock skew can report a publish time slightly ahead of now.
		// Negative age must clamp rather than run the exponential above 1+k.
		if got, want := Freshness(now.Add(time.Hour), now, 3, 120), 4.0; got != want {
			t.Errorf("Freshness = %v, want %v", got, want)
		}
	})
}

func TestDispatchOrder(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	order := func(c Campaign) float64 {
		return c.DispatchOrder(now, DefaultFreshnessK, DefaultFreshnessTauHours)
	}
	// A fresh release of a mid-tier package can outrank a stale version of a
	// critical one. That mobility is the point of deriving recency at read time.
	freshMidTier := Campaign{Score: 0.30, Published: now.Add(-time.Hour)}
	staleCritical := Campaign{Score: 0.99, Published: now.AddDate(-1, 0, 0)}
	if order(freshMidTier) <= order(staleCritical) {
		t.Errorf("fresh mid-tier (%v) should outrank stale critical (%v)",
			order(freshMidTier), order(staleCritical))
	}
	freshCritical := Campaign{Score: 0.99, Published: now.Add(-time.Hour)}
	if order(freshCritical) <= order(freshMidTier) {
		t.Error("a fresh critical package must still outrank a fresh mid-tier one")
	}
	// The boost decays in the queue: the same campaign ordered a month later
	// has lost its spike.
	if later := freshMidTier.DispatchOrder(now.AddDate(0, 1, 0), DefaultFreshnessK, DefaultFreshnessTauHours); later >= order(freshMidTier) {
		t.Errorf("dispatch order must decay while queued: %v then %v", order(freshMidTier), later)
	}
}
