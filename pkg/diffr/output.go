// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"strings"
)

// DiffNode represents a single node in the difference tree.
// Intended for approximate compatibility with diffoscope's JSON schema.
type DiffNode struct {
	Source1     string     `json:"source1"`
	Source2     string     `json:"source2"`
	UnifiedDiff *string    `json:"unified_diff,omitempty"`
	Comments    []string   `json:"comments,omitempty"`
	Details     []DiffNode `json:"details,omitempty"` // Recursive nodes
}

const (
	detailGlyph  = "│ "
	branchGlyph  = "├── "
	commentGlyph = "│┄ "
)

func (node DiffNode) String() string {
	var builder strings.Builder
	// Write the main diff header from the root node
	builder.WriteString("--- ")
	builder.WriteString(node.Source1)
	builder.WriteByte('\n')
	builder.WriteString("+++ ")
	builder.WriteString(node.Source2)
	builder.WriteByte('\n')
	// Write top-level comments from the root node
	for _, comment := range node.Comments {
		builder.WriteString(commentGlyph)
		builder.WriteString(comment)
		builder.WriteByte('\n')
	}
	// Write root node's unified diff if present
	if node.UnifiedDiff != nil {
		content := strings.TrimSuffix(*node.UnifiedDiff, "\n")
		for line := range strings.SplitSeq(content, "\n") {
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
	}
	// Recurse to format root node's Details
	formatDetails(&builder, node.Details, "", 0)
	return builder.String()
}

// formatDetails is a recursive helper that walks the diff tree.
func formatDetails(builder *strings.Builder, nodes []DiffNode, prefix string, depth int) {
	for _, node := range nodes {
		hasDetails := len(node.Details) > 0
		if hasDetails && depth == 0 && node.UnifiedDiff == nil {
			// Format header for containers
			builder.WriteString(prefix)
			builder.WriteString(detailGlyph)
			builder.WriteString("  --- ")
			builder.WriteString(node.Source1)
			builder.WriteString("\n")
			builder.WriteString(prefix)
			builder.WriteString(branchGlyph)
			builder.WriteString("+++ ")
			builder.WriteString(node.Source2)
			builder.WriteByte('\n')
			nextPrefix := prefix + detailGlyph
			formatDetails(builder, node.Details, nextPrefix, depth+1)
		} else {
			// Standard formatting for all other diffs
			builder.WriteString(prefix)
			builder.WriteString(branchGlyph)
			builder.WriteString(node.Source1)
			builder.WriteByte('\n')
			// Content prefix for nested content
			contentPrefix := prefix + detailGlyph
			// Format and print any comments for this specific node
			if len(node.Comments) > 0 {
				commentPrefix := strings.TrimSuffix(contentPrefix, detailGlyph) + commentGlyph
				for _, comment := range node.Comments {
					builder.WriteString(commentPrefix)
					builder.WriteString(comment)
					builder.WriteByte('\n')
				}
			}
			// Handle nested details or unified diff
			if hasDetails {
				formatDetails(builder, node.Details, contentPrefix, depth+1)
			} else if node.UnifiedDiff != nil {
				content := strings.TrimSuffix(*node.UnifiedDiff, "\n")
				for line := range strings.SplitSeq(content, "\n") {
					builder.WriteString(contentPrefix)
					builder.WriteString(line)
					builder.WriteByte('\n')
				}
			}
		}
	}
}
