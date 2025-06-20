// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package bufiox

import (
	"fmt"
	"testing"
)

func TestLineBuffer(t *testing.T) {
	t.Run("WriteAndRead", func(t *testing.T) {
		testCases := []struct {
			name     string
			capacity int
			writes   []string
			expected string
		}{
			{
				name:     "single line",
				capacity: 100,
				writes:   []string{"Hello, World!\n"},
				expected: "Hello, World!\n",
			},
			{
				name:     "multiple lines",
				capacity: 100,
				writes:   []string{"First line\n", "Second line\n", "Third line\n"},
				expected: "First line\nSecond line\nThird line\n",
			},
			{
				name:     "empty writes",
				capacity: 50,
				writes:   []string{"", "Hello\n", "", "World\n"},
				expected: "Hello\nWorld\n",
			},
			{
				name:     "no newlines",
				capacity: 100,
				writes:   []string{"no", "newline", "here"},
				expected: "nonewlinehere",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				// Write all data
				for _, write := range tc.writes {
					_, err := lb.Write([]byte(write))
					if err != nil {
						t.Fatalf("Write failed: %v", err)
					}
				}
				// Read back
				buf := make([]byte, tc.capacity*2)
				n, err := lb.Read(buf)
				if err != nil {
					t.Fatalf("Read failed: %v", err)
				}
				if string(buf[:n]) != tc.expected {
					t.Errorf("Expected %q, got %q", tc.expected, string(buf[:n]))
				}
			})
		}
	})

	t.Run("EmptyBuffer", func(t *testing.T) {
		capacities := []int{10, 50, 100, 1000}

		for _, capacity := range capacities {
			t.Run(fmt.Sprintf("capacity_%d", capacity), func(t *testing.T) {
				lb := NewLineBuffer(capacity)
				buf := make([]byte, 10)
				n, err := lb.Read(buf)
				if n != 0 || err != nil {
					t.Errorf("Expected (0, nil), got (%d, %v)", n, err)
				}
			})
		}
	})

	t.Run("LineEviction", func(t *testing.T) {
		testCases := []struct {
			name          string
			capacity      int
			lines         []string
			expectedLines []string // Lines that should remain after eviction
		}{
			{
				name:          "evict first line",
				capacity:      20,
				lines:         []string{"Line1\n", "Line2\n", "Line3\n", "Line4\n"},
				expectedLines: []string{"Line2\n", "Line3\n", "Line4\n"},
			},
			{
				name:          "evict multiple lines",
				capacity:      6,
				lines:         []string{"A\n", "B\n", "C\n", "D\n", "E\n", "F\n"},
				expectedLines: []string{"D\n", "E\n", "F\n"},
			},
			{
				name:          "no eviction needed",
				capacity:      50,
				lines:         []string{"Short\n", "Lines\n"},
				expectedLines: []string{"Short\n", "Lines\n"},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				// Write all lines
				for _, line := range tc.lines {
					lb.Write([]byte(line))
				}
				// Read all content
				buf := make([]byte, tc.capacity*2)
				n, _ := lb.Read(buf)
				expected := ""
				for _, line := range tc.expectedLines {
					expected += line
				}
				if string(buf[:n]) != expected {
					t.Errorf("Expected %q, got %q", expected, string(buf[:n]))
				}
			})
		}
	})

	t.Run("PartialLines", func(t *testing.T) {
		testCases := []struct {
			name     string
			capacity int
			parts    []string
			expected string
		}{
			{
				name:     "simple partial",
				capacity: 100,
				parts:    []string{"Partial", " line\n"},
				expected: "Partial line\n",
			},
			{
				name:     "multiple parts",
				capacity: 100,
				parts:    []string{"A", "B", "C", "D", "\n", "E", "F\nG", "\n"},
				expected: "ABCD\nEF\nG\n",
			},
			{
				name:     "mixed complete and partial",
				capacity: 100,
				parts:    []string{"Complete\n", "Part", "ial\n"},
				expected: "Complete\nPartial\n",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				// Write all parts
				for _, part := range tc.parts {
					lb.Write([]byte(part))
				}
				// Read back
				buf := make([]byte, tc.capacity)
				n, _ := lb.Read(buf)
				if string(buf[:n]) != tc.expected {
					t.Errorf("Expected %q, got %q", tc.expected, string(buf[:n]))
				}
			})
		}
	})

	t.Run("WrapAround", func(t *testing.T) {
		testCases := []struct {
			name          string
			capacity      int
			initialWrites []string
			readSize      int
			laterWrites   []string
			expectedReads []string
		}{
			{
				name:          "basic wrap",
				capacity:      20,
				initialWrites: []string{"12345678\n", "abcdefgh\n"},
				readSize:      9,
				laterWrites:   []string{"WRAP\n"},
				expectedReads: []string{"abcdefgh\n", "WRAP\n"},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				// Initial writes
				for _, write := range tc.initialWrites {
					lb.Write([]byte(write))
				}
				// First read
				buf := make([]byte, tc.readSize)
				lb.Read(buf)
				// Later writes
				for _, write := range tc.laterWrites {
					lb.Write([]byte(write))
				}
				// Read remaining data
				for i, expected := range tc.expectedReads {
					n, _ := lb.Read(buf)
					if string(buf[:n]) != expected {
						t.Errorf("Read %d: expected %q, got %q", i, expected, string(buf[:n]))
					}
				}
			})
		}
	})

	t.Run("BufferTooSmall", func(t *testing.T) {
		testCases := []struct {
			name     string
			capacity int
			data     string
			wantErr  bool
		}{
			{
				name:     "line too large",
				capacity: 10,
				data:     "This line is too long\n",
				wantErr:  true,
			},
			{
				name:     "exactly fits",
				capacity: 10,
				data:     "1234567890",
				wantErr:  false,
			},
			{
				name:     "fits with newline",
				capacity: 11,
				data:     "1234567890\n",
				wantErr:  false,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				lb := NewLineBuffer(tc.capacity)
				_, err := lb.Write([]byte(tc.data))
				if tc.wantErr && err == nil {
					t.Error("Expected error but got none")
				}
				if !tc.wantErr && err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			})
		}
	})
}
