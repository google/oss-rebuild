// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

// PackageSignal is the read-time join of the priority exports for one
// package. A package missing from one export carries zeros for it.
type PackageSignal struct {
	Ecosystem  string
	Package    string
	Dependents int64   // distinct packages depending on this one, directly or transitively
	Prevalence float64 // Dependents on a log scale against the ecosystem's top package, in (0,1]
	Model      string
	Rating     float64 // Model's prominence answer, scaled to [0,1]
	Prominence float64 // Rating's per-ecosystem rank quantile, in [0,1]
	Score      float64 // combined priority across the signals present
}

// JoinSignals merges the package-level prevalence rows with the prominence
// rows into one PackageSignal per (ecosystem, package).
func JoinSignals(prevs []PrevalenceRecord, proms []ProminenceRecord) []PackageSignal {
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
	for _, r := range proms {
		i := at(r.Ecosystem, r.Package)
		out[i].Model, out[i].Rating, out[i].Prominence = r.Model, r.Rating, r.Prominence
	}
	for i := range out {
		out[i].Score = Score(out[i].Prevalence, out[i].Prominence)
	}
	return out
}
