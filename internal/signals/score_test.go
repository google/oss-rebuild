// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLogScale(t *testing.T) {
	for _, tc := range []struct {
		name       string
		count, max int64
		want       float64
	}{
		{"Top", 9000, 9000, 1.0},
		{"Single", 1, 9000, math.Log1p(1) / math.Log1p(9000)},
		{"AboveMax", 10000, 9000, 1.0},
		{"ZeroCount", 0, 9000, 0},
		{"EmptyPopulation", 5, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, LogScale(tc.count, tc.max)); diff != "" {
				t.Errorf("LogScale(%d, %d) mismatch (-want +got):\n%s", tc.count, tc.max, diff)
			}
		})
	}
}
