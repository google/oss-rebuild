// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestJoinSignals(t *testing.T) {
	got := JoinSignals(
		[]PrevalenceRecord{
			{Ecosystem: "pypi", Package: "both", Dependents: 100, Prevalence: 0.8},
			// Version-level rows never join: the merge is per package.
			{Ecosystem: "pypi", Package: "both", Version: "1.0", Dependents: 40, Prevalence: 0.75},
			{Ecosystem: "pypi", Package: "prevonly", Dependents: 10, Prevalence: 0.4},
		},
		[]ProminenceRecord{
			{Ecosystem: "pypi", Package: "both", Model: "m", Rating: 0.9, Prominence: 0.6},
			{Ecosystem: "pypi", Package: "promonly", Rating: 0.5, Prominence: 0.2},
			{Ecosystem: "npm", Package: "both", Rating: 0.1, Prominence: 0.1},
		},
	)
	want := []PackageSignal{
		{Ecosystem: "pypi", Package: "both", Dependents: 100, Prevalence: 0.8, Model: "m", Rating: 0.9, Prominence: 0.6, Score: 0.7},
		{Ecosystem: "pypi", Package: "prevonly", Dependents: 10, Prevalence: 0.4, Score: 0.2},
		{Ecosystem: "pypi", Package: "promonly", Rating: 0.5, Prominence: 0.2, Score: 0.1},
		{Ecosystem: "npm", Package: "both", Rating: 0.1, Prominence: 0.1, Score: 0.05},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("JoinSignals mismatch (-want +got):\n%s", diff)
	}
}
