// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestBudgetExpired(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		start  time.Time
		period time.Duration
		want   bool
	}{
		{"WithinPeriod", now.Add(-time.Hour), 24 * time.Hour, false},
		{"PastPeriod", now.Add(-25 * time.Hour), 24 * time.Hour, true},
		{"NoPeriodConfigured", now.Add(-1000 * time.Hour), 0, false},
		// A zero start marks a ledger that has never been initialized, which
		// rollover handles by seeding it, not by expiring it.
		{"UninitializedLedger", time.Time{}, 24 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := Budget{PeriodStart: tc.start}
			if diff := cmp.Diff(tc.want, b.Expired(now, tc.period)); diff != "" {
				t.Errorf("Expired mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
