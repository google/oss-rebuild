// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

// PackageSignal is the read-time join of the priority exports for one
// package: each signal's raw measure and normalized score, plus the
// combined Score.
type PackageSignal struct {
	Ecosystem  string
	Package    string
	Dependents int64   // distinct packages depending on this one, directly or transitively
	Prevalence float64 // Dependents on a log scale against the ecosystem's top package, in (0,1]
	Score      float64 // combined priority across the signals present
}

// JoinSignals folds the package-level rows of the priority exports into
// one PackageSignal per (ecosystem, package). Version-level prevalence
// rows are skipped: the join is the per-package view, and version
// specialization happens where a version is in hand.
func JoinSignals(prevs []PrevalenceRecord) []PackageSignal {
	type key struct{ eco, pkg string }
	idx := make(map[key]int)
	var out []PackageSignal
	at := func(eco, pkg string) int {
		k := key{eco, pkg}
		if i, ok := idx[k]; ok {
			return i
		}
		idx[k] = len(out)
		out = append(out, PackageSignal{Ecosystem: eco, Package: pkg})
		return len(out) - 1
	}
	for _, r := range prevs {
		if r.Version != "" {
			continue
		}
		i := at(r.Ecosystem, r.Package)
		out[i].Dependents, out[i].Prevalence = r.Dependents, r.Prevalence
	}
	for i := range out {
		// Prevalence is the only signal so far, so it is the score.
		out[i].Score = out[i].Prevalence
	}
	return out
}
