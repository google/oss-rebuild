// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

import "math"

// Score combines a package's signals, each normalized per ecosystem to [0,1],
// into one rebuild priority. Scoring a version uses its own prevalence. The
// two signals are weighted equally because there is no data yet to tune the
// weights, so a package with only one of them scores at most 0.5.
func Score(prevalence, prominence float64) float64 { return 0.5*prevalence + 0.5*prominence }

// LogScale maps a dependent count onto (0,1] by its order of magnitude
// relative to max, the ecosystem's most depended-on package:
// ln(1+count)/ln(1+max). Dependent counts are tail heavy, so a rank quantile
// over a whole ecosystem crowds every exported row above 0.99 where a log
// scale spreads them by blast radius. A count at or above max scores 1 and
// max <= 0 yields 0.
func LogScale(count, max int64) float64 {
	if max <= 0 || count <= 0 {
		return 0
	}
	return min(1, math.Log1p(float64(count))/math.Log1p(float64(max)))
}

// Percentile maps a 0-based descending rank within a population of n onto
// (0,1], where the top-ranked entry scores highest. n <= 0 yields 0.
func Percentile(rank, n int) float64 {
	if n <= 0 {
		return 0
	}
	return float64(n-rank) / float64(n)
}
