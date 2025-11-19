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
	detailGlyph       = "│ "
	branchGlyph       = "├── "
	branchCornerGlyph = "├─┐ "
	commentGlyph      = "│┄ "
)

func (node DiffNode) String() string {
	var builder lineBuilder
	// Write the main diff header from the root node
	builder.WriteLine("--- ", node.Source1)
	builder.WriteLine("+++ ", node.Source2)
	// Write top-level comments from the root node
	for _, comment := range node.Comments {
		builder.WriteLine(commentGlyph, comment)
	}
	// Write root node's unified diff if present
	if node.UnifiedDiff != nil {
		builder.WriteLine(*node.UnifiedDiff)
	}
	// Recurse to format root node's Details
	formatDetails(&builder, node.Details, "", 0)
	return builder.String()
}

// formatDetails is a recursive helper that walks the diff tree.
func formatDetails(builder *lineBuilder, nodes []DiffNode, prefix string, depth int) {
	for _, node := range nodes {
		hasDetails := len(node.Details) > 0
		if hasDetails && depth == 0 && node.UnifiedDiff == nil {
			// Format header for containers
			builder.WriteLine(prefix, detailGlyph, "  --- ", node.Source1)
			builder.WriteLine(prefix, branchCornerGlyph, "+++ ", node.Source2)
			nextPrefix := prefix + detailGlyph
			formatDetails(builder, node.Details, nextPrefix, depth+1)
		} else {
			// Standard formatting for all other diffs
			builder.WriteLine(prefix, branchGlyph, node.Source1)
			// Format and print any comments for this specific node
			if len(node.Comments) > 0 {
				for _, comment := range node.Comments {
					builder.WriteLine(prefix, commentGlyph, comment)
				}
			}
			// Handle nested details or unified diff
			if hasDetails {
				formatDetails(builder, node.Details, prefix+detailGlyph, depth+1)
			} else if node.UnifiedDiff != nil {
				content := strings.TrimSuffix(*node.UnifiedDiff, "\n")
				for line := range strings.SplitSeq(content, "\n") {
					builder.WriteLine(prefix, detailGlyph, line)
				}
			}
		}
	}
}

type lineBuilder struct {
	strings.Builder
}

func (b *lineBuilder) WriteLine(s ...string) {
	for _, st := range s {
		b.WriteString(st)
	}
	b.WriteByte('\n')
}
