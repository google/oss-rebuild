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
// Returns "mixed" if multiple line ending types are present
func detectLineEndings(data []byte) string {
	var hasCRLF, hasCR, hasLF bool
	for i := range data {
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			hasCRLF = true
		}
		if data[i] == '\r' && (i+1 >= len(data) || data[i+1] != '\n') {
			hasCR = true
		}
		if data[i] == '\n' && (i == 0 || data[i-1] != '\r') {
			hasLF = true
		}
	}
	// Count how many types we found
	count := 0
	if hasCRLF {
		count++
	}
	if hasCR {
		count++
	}
	if hasLF {
		count++
	}
	switch {
	case count > 1:
		return "mixed"
	case hasCRLF:
		return "CRLF"
	case hasCR:
		return "CR"
	case hasLF:
		return "LF"
	default:
		return "none"
	}
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
	lineEndingsMixed := lineend1 == "mixed" && lineend2 == "mixed"
	if lineEndingsDiffer {
		node.Comments = append(node.Comments, fmt.Sprintf("Line endings differ (-%s,+%s)", lineend1, lineend2))
	} else if lineEndingsMixed {
		node.Comments = append(node.Comments, "WARNING: Files have mixed line endings which are not shown in diff")
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
			node.Comments = append(node.Comments, "Diff shown with normalized line endings")
		}
	}
	return false, nil
}
