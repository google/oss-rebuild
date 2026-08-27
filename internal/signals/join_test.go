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
			{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8},
			// Version-level rows never join: the merge is per package.
			{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Dependents: 40, Prevalence: 0.75},
			{Ecosystem: "pypi", Package: "pkgB", Dependents: 10, Prevalence: 0.4},
		},
	)
	want := []PackageSignal{
		{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8, Score: 0.8},
		{Ecosystem: "pypi", Package: "pkgB", Dependents: 10, Prevalence: 0.4, Score: 0.4},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("JoinSignals mismatch (-want +got):\n%s", diff)
	}
}
