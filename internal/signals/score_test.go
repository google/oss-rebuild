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

func TestPercentile(t *testing.T) {
	for _, tc := range []struct {
		name string
		rank int
		n    int
		want float64
	}{
		{"TopOfTen", 0, 10, 1.0},
		{"LastOfTen", 9, 10, 0.1},
		{"MedianOfFour", 1, 4, 0.75},
		{"SoleMember", 0, 1, 1.0},
		{"EmptyPopulation", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Percentile(tc.rank, tc.n)); diff != "" {
				t.Errorf("Percentile(%d, %d) mismatch (-want +got):\n%s", tc.rank, tc.n, diff)
			}
		})
	}
}

func TestScore(t *testing.T) {
	for _, tc := range []struct {
		name         string
		qPrev, qProm float64
		want         float64
	}{
		{"BothSignals", 0.8, 0.6, 0.7},
		// One signal alone scores at most half.
		{"PrevalenceOnly", 0.8, 0, 0.4},
		{"ProminenceOnly", 0, 0.8, 0.4},
		{"NoSignals", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, Score(tc.qPrev, tc.qProm)); diff != "" {
				t.Errorf("Score mismatch (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("LeafAppOutranksObscureLibrary", func(t *testing.T) {
		// A leaf application must outrank a library with a few dependents.
		if leafApp, obscureLibrary := Score(0, 1.0), Score(0.2, 0); leafApp <= obscureLibrary {
			t.Errorf("leaf app (%v) should outrank an obscure library (%v)", leafApp, obscureLibrary)
		}
	})
}
