// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

// checkOrderConsistency checks if two entry orders are consistent
// (i.e., common entries appear in the same relative order, ignoring insertions/deletions)
func checkOrderConsistency(order1, order2 []string) bool {
	// Build position maps for common entries
	pos1 := make(map[string]int)
	for i, name := range order1 {
		pos1[name] = i
	}
	pos2 := make(map[string]int)
	for i, name := range order2 {
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
			// In order1: does common[i] come before common[j]?
			order1Consistent := pos1[common[i]] < pos1[common[j]]
			// In order2: does common[i] come before common[j]?
			order2Consistent := pos2[common[i]] < pos2[common[j]]
			// If relative order differs, they're inconsistent
			if order1Consistent != order2Consistent {
				return false
			}
		}
	}
	return true
}
