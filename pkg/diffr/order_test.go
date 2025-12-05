// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import "testing"

func TestCheckOrderConsistency(t *testing.T) {
	tests := []struct {
		name     string
		order1   []string
		order2   []string
		expected bool
	}{
		{
			name:     "identical orders",
			order1:   []string{"a", "b", "c"},
			order2:   []string{"a", "b", "c"},
			expected: true,
		},
		{
			name:     "empty orders",
			order1:   []string{},
			order2:   []string{},
			expected: true,
		},
		{
			name:     "one empty order",
			order1:   []string{"a", "b"},
			order2:   []string{},
			expected: true, // No common elements means consistent
		},
		{
			name:     "single common element",
			order1:   []string{"a"},
			order2:   []string{"a"},
			expected: true,
		},
		{
			name:     "insertion at beginning",
			order1:   []string{"a", "b", "c"},
			order2:   []string{"x", "a", "b", "c"},
			expected: true, // Common elements (a,b,c) maintain order
		},
		{
			name:     "insertion at end",
			order1:   []string{"a", "b", "c"},
			order2:   []string{"a", "b", "c", "x"},
			expected: true,
		},
		{
			name:     "insertion in middle",
			order1:   []string{"a", "c"},
			order2:   []string{"a", "b", "c"},
			expected: true, // Common elements (a,c) maintain order
		},
		{
			name:     "deletion at beginning",
			order1:   []string{"x", "a", "b", "c"},
			order2:   []string{"a", "b", "c"},
			expected: true,
		},
		{
			name:     "deletion at end",
			order1:   []string{"a", "b", "c", "x"},
			order2:   []string{"a", "b", "c"},
			expected: true,
		},
		{
			name:     "deletion in middle",
			order1:   []string{"a", "b", "c"},
			order2:   []string{"a", "c"},
			expected: true,
		},
		{
			name:     "multiple insertions and deletions",
			order1:   []string{"x", "a", "y", "b", "z", "c"},
			order2:   []string{"a", "m", "b", "n", "c", "o"},
			expected: true, // Common elements (a,b,c) maintain order
		},
		{
			name:     "simple swap",
			order1:   []string{"a", "b"},
			order2:   []string{"b", "a"},
			expected: false, // Order reversed
		},
		{
			name:     "swap with extra elements",
			order1:   []string{"a", "b", "c"},
			order2:   []string{"b", "a", "c"},
			expected: false, // a and b swapped
		},
		{
			name:     "partial reorder",
			order1:   []string{"a", "b", "c", "d"},
			order2:   []string{"a", "c", "b", "d"},
			expected: false, // b and c swapped
		},
		{
			name:     "complete reversal",
			order1:   []string{"a", "b", "c"},
			order2:   []string{"c", "b", "a"},
			expected: false,
		},
		{
			name:     "one element moved forward",
			order1:   []string{"a", "b", "c", "d"},
			order2:   []string{"c", "a", "b", "d"},
			expected: false, // c moved before a and b
		},
		{
			name:     "one element moved backward",
			order1:   []string{"a", "b", "c", "d"},
			order2:   []string{"a", "c", "d", "b"},
			expected: false, // b moved after c and d
		},
		{
			name:     "no common elements",
			order1:   []string{"a", "b", "c"},
			order2:   []string{"x", "y", "z"},
			expected: true, // No common elements means consistent
		},
		{
			name:     "interleaved additions preserving order",
			order1:   []string{"a", "b", "c", "d", "e"},
			order2:   []string{"x", "a", "y", "b", "z", "c", "w", "d", "v", "e", "u"},
			expected: true, // Common elements (a,b,c,d,e) maintain order
		},
		{
			name:     "three elements with complex swap",
			order1:   []string{"a", "b", "c"},
			order2:   []string{"b", "c", "a"},
			expected: false, // Rotation is a reorder
		},
		{
			name:     "large list with one swap",
			order1:   []string{"a", "b", "c", "d", "e", "f", "g", "h"},
			order2:   []string{"a", "b", "c", "e", "d", "f", "g", "h"},
			expected: false, // d and e swapped
		},
		{
			name:     "common element at different relative positions",
			order1:   []string{"a", "b"},
			order2:   []string{"c", "b", "d", "a"},
			expected: false, // b comes before a in order2, after in order1
		},
		// Duplicate detection tests
		{
			name:     "identical orders with duplicates",
			order1:   []string{"a", "b", "a"},
			order2:   []string{"a", "b", "a"},
			expected: true, // Identical orders are consistent even with duplicates
		},
		{
			name:     "duplicate in order1",
			order1:   []string{"a", "b", "a"},
			order2:   []string{"a", "b"},
			expected: false, // Duplicates cause order inconsistency
		},
		{
			name:     "duplicate in order2",
			order1:   []string{"a", "b"},
			order2:   []string{"a", "b", "a"},
			expected: false, // Duplicates cause order inconsistency
		},
		{
			name:     "duplicates in both orders",
			order1:   []string{"a", "b", "a"},
			order2:   []string{"b", "a", "b"},
			expected: false, // Duplicates cause order inconsistency
		},
		{
			name:     "duplicate at beginning",
			order1:   []string{"x", "x", "a", "b"},
			order2:   []string{"a", "b"},
			expected: false, // Duplicate detected immediately
		},
		{
			name:     "multiple different duplicates",
			order1:   []string{"a", "b", "c", "a", "b"},
			order2:   []string{"a", "b", "c"},
			expected: false, // Multiple duplicates still inconsistent
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkOrderConsistency(tt.order1, tt.order2)
			if result != tt.expected {
				t.Errorf("checkOrderConsistency(%v, %v) = %v, want %v",
					tt.order1, tt.order2, result, tt.expected)
			}
		})
	}
}
