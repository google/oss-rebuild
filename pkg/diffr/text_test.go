// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"bytes"
	"testing"
)

func TestDetectLineEndings(t *testing.T) {
	testCases := []struct {
		name     string
		data     []byte
		expected string
	}{
		{
			name:     "empty_file",
			data:     []byte(""),
			expected: "none",
		},
		{
			name:     "no_line_endings",
			data:     []byte("hello world"),
			expected: "none",
		},
		{
			name:     "unix_lf_only",
			data:     []byte("line1\nline2\nline3\n"),
			expected: "LF",
		},
		{
			name:     "windows_crlf_only",
			data:     []byte("line1\r\nline2\r\nline3\r\n"),
			expected: "CRLF",
		},
		{
			name:     "classic_mac_cr_only",
			data:     []byte("line1\rline2\rline3\r"),
			expected: "CR",
		},
		{
			name:     "mixed_lf_and_crlf",
			data:     []byte("line1\nline2\r\nline3\n"),
			expected: "mixed",
		},
		{
			name:     "mixed_cr_and_lf",
			data:     []byte("line1\rline2\nline3\r"),
			expected: "mixed",
		},
		{
			name:     "mixed_crlf_and_cr",
			data:     []byte("line1\r\nline2\rline3\r\n"),
			expected: "mixed",
		},
		{
			name:     "mixed_all_three",
			data:     []byte("line1\nline2\r\nline3\rline4\n"),
			expected: "mixed",
		},
		{
			name:     "single_lf",
			data:     []byte("hello\n"),
			expected: "LF",
		},
		{
			name:     "single_crlf",
			data:     []byte("hello\r\n"),
			expected: "CRLF",
		},
		{
			name:     "single_cr",
			data:     []byte("hello\r"),
			expected: "CR",
		},
		{
			name:     "crlf_at_end_only",
			data:     []byte("no newlines here\r\n"),
			expected: "CRLF",
		},
		{
			name:     "lf_in_middle",
			data:     []byte("hello\nworld"),
			expected: "LF",
		},
		{
			name:     "multiple_consecutive_lf",
			data:     []byte("line1\n\n\nline2\n"),
			expected: "LF",
		},
		{
			name:     "multiple_consecutive_crlf",
			data:     []byte("line1\r\n\r\n\r\nline2\r\n"),
			expected: "CRLF",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := detectLineEndings(tc.data)
			if result != tc.expected {
				t.Errorf("detectLineEndings(%q) = %q, want %q", tc.data, result, tc.expected)
			}
		})
	}
}

func TestNormalizeToLF(t *testing.T) {
	testCases := []struct {
		name     string
		input    []byte
		expected []byte
	}{
		{
			name:     "already_lf",
			input:    []byte("line1\nline2\nline3\n"),
			expected: []byte("line1\nline2\nline3\n"),
		},
		{
			name:     "crlf_to_lf",
			input:    []byte("line1\r\nline2\r\nline3\r\n"),
			expected: []byte("line1\nline2\nline3\n"),
		},
		{
			name:     "cr_to_lf",
			input:    []byte("line1\rline2\rline3\r"),
			expected: []byte("line1\nline2\nline3\n"),
		},
		{
			name:     "mixed_to_lf",
			input:    []byte("line1\nline2\r\nline3\rline4\n"),
			expected: []byte("line1\nline2\nline3\nline4\n"),
		},
		{
			name:     "empty",
			input:    []byte(""),
			expected: []byte(""),
		},
		{
			name:     "no_line_endings",
			input:    []byte("hello world"),
			expected: []byte("hello world"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := normalizeToLF(tc.input)
			if string(result) != string(tc.expected) {
				t.Errorf("normalizeToLF(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestCompareText_LineEndings(t *testing.T) {
	testCases := []struct {
		name           string
		content1       []byte
		content2       []byte
		expectMatch    bool
		expectComments []string
		expectDiff     bool
	}{
		{
			name:        "identical_with_lf",
			content1:    []byte("line1\nline2\n"),
			content2:    []byte("line1\nline2\n"),
			expectMatch: true,
		},
		{
			name:           "same_content_different_line_endings",
			content1:       []byte("line1\nline2\n"),
			content2:       []byte("line1\r\nline2\r\n"),
			expectMatch:    false,
			expectComments: []string{"Line endings differ (-LF,+CRLF)"},
			expectDiff:     false,
		},
		{
			name:           "different_content_same_line_endings",
			content1:       []byte("line1\nline2\n"),
			content2:       []byte("line1\nline3\n"),
			expectMatch:    false,
			expectComments: []string{},
			expectDiff:     true,
		},
		{
			name:        "different_content_different_line_endings",
			content1:    []byte("line1\nline2\n"),
			content2:    []byte("line1\r\nline3\r\n"),
			expectMatch: false,
			expectComments: []string{
				"Line endings differ (-LF,+CRLF)",
				"Diff shown with normalized line endings",
			},
			expectDiff: true,
		},
		{
			name:           "both_files_mixed_line_endings",
			content1:       []byte("line1\nline2\r\n"),
			content2:       []byte("line1\r\nline2\n"),
			expectMatch:    false,
			expectComments: []string{"WARNING: Files have mixed line endings which are not shown in diff"},
			expectDiff:     false,
		},
		{
			name:           "one_file_mixed_one_pure",
			content1:       []byte("line1\nline2\r\n"),
			content2:       []byte("line1\nline2\n"),
			expectMatch:    false,
			expectComments: []string{"Line endings differ (-mixed,+LF)"},
			expectDiff:     false,
		},
		{
			name:        "mixed_with_content_diff",
			content1:    []byte("line1\nline2\r\n"),
			content2:    []byte("line1\r\nline3\n"),
			expectMatch: false,
			expectComments: []string{
				"WARNING: Files have mixed line endings which are not shown in diff",
			},
			expectDiff: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			node := &DiffNode{}
			file1 := File{Name: "file1", Reader: bytes.NewReader(tc.content1)}
			file2 := File{Name: "file2", Reader: bytes.NewReader(tc.content2)}
			match, err := compareText(node, file1, file2)
			if err != nil {
				t.Fatalf("compareText() error = %v", err)
			}
			if match != tc.expectMatch {
				t.Errorf("compareText() match = %v, want %v", match, tc.expectMatch)
			}
			// Check comments
			if len(tc.expectComments) != len(node.Comments) {
				t.Errorf("compareText() comments count = %d, want %d\nGot: %v\nWant: %v",
					len(node.Comments), len(tc.expectComments), node.Comments, tc.expectComments)
			} else {
				for i, expected := range tc.expectComments {
					if node.Comments[i] != expected {
						t.Errorf("compareText() comment[%d] = %q, want %q", i, node.Comments[i], expected)
					}
				}
			}
			// Check if diff was generated
			hasDiff := node.UnifiedDiff != nil
			if hasDiff != tc.expectDiff {
				t.Errorf("compareText() has diff = %v, want %v", hasDiff, tc.expectDiff)
			}
		})
	}
}
