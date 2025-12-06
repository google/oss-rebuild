// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"bytes"
	"fmt"

	"github.com/google/oss-rebuild/internal/gitdiff"
	"github.com/pkg/errors"
)

// detectLineEndings detects the line ending type in the given data
func detectLineEndings(data []byte) string {
	// Check for CRLF first (to distinguish from separate CR/LF)
	if bytes.Contains(data, []byte("\r\n")) {
		return "CRLF"
	}
	// Check for CR only (classic Mac)
	if bytes.Contains(data, []byte("\r")) {
		return "CR"
	}
	// Check for LF (Unix/Linux)
	if bytes.Contains(data, []byte("\n")) {
		return "LF"
	}
	return "none"
}

// normalizeToLF converts all line endings to LF (Unix standard)
func normalizeToLF(data []byte) []byte {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	return data
}

// compareText compares two text files and generates unified diff
func compareText(node *DiffNode, file1, file2 File) (bool, error) {
	// Read both files
	content1, err := readAll(file1.Reader)
	if err != nil {
		return false, errors.Wrap(err, "reading file1")
	}
	content2, err := readAll(file2.Reader)
	if err != nil {
		return false, errors.Wrap(err, "reading file2")
	}
	// Check if identical
	if bytes.Equal(content1, content2) {
		return true, nil
	}
	// Detect and note line endings
	lineend1 := detectLineEndings(content1)
	lineend2 := detectLineEndings(content2)
	lineEndingsDiffer := lineend1 != lineend2 && lineend1 != "none" && lineend2 != "none"
	if lineEndingsDiffer {
		node.Comments = append(node.Comments,
			fmt.Sprintf("Line endings differ (-%s,+%s)", lineend1, lineend2))
	}
	// Normalize both to LF for comparison
	normalized1 := normalizeToLF(content1)
	normalized2 := normalizeToLF(content2)
	if bytes.Equal(normalized1, normalized2) {
		// Files differ only in line endings
		return false, nil
	}
	// Generate unified diff using normalized content
	diff, err := gitdiff.Strings(string(normalized1), string(normalized2))
	if err != nil {
		return false, errors.Wrap(err, "generating diff")
	}
	if diff != "" {
		node.UnifiedDiff = &diff
		// Add second comment about normalization if line endings differed
		if lineEndingsDiffer {
			node.Comments = append(node.Comments,
				"Diff shown with normalized line endings")
		}
	}
	return false, nil
}
