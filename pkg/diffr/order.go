// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import "slices"

// checkOrderConsistency checks if two entry orders are consistent
// (i.e., common entries appear in the same relative order, ignoring insertions/deletions)
// Identical orders (including duplicates) considered consistent.
// Duplicate entries (with order differences) considered inconsistent.
func checkOrderConsistency(order1, order2 []string) bool {
	// Quick check: if orders are identical, they're consistent
	if slices.Equal(order1, order2) {
		return true
	}
	// Build position maps, checking for duplicates during construction
	pos1 := make(map[string]int)
	for i, name := range order1 {
		if _, exists := pos1[name]; exists {
			return false // Duplicate found
		}
		pos1[name] = i
	}
	pos2 := make(map[string]int)
	for i, name := range order2 {
		if _, exists := pos2[name]; exists {
			return false // Duplicate found
		}
		pos2[name] = i
	}
	// Find common entries
	var common []string
	for name := range pos1 {
		if _, exists := pos2[name]; exists {
			common = append(common, name)
		}
	}
	// Check if common entries maintain relative order
	for i := range common {
		for j := i + 1; j < len(common); j++ {
			order1Consistent := pos1[common[i]] < pos1[common[j]]
			order2Consistent := pos2[common[i]] < pos2[common[j]]
			if order1Consistent != order2Consistent {
				return false
			}
		}
	}
	return true
}
