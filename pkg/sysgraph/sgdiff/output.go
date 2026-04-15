// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sgdiff

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// OutputOptions controls output formatting.
type OutputOptions struct {
	// MaxItemsPerCategory limits items per section (0 = unlimited).
	MaxItemsPerCategory int
	// Verbose shows all items without truncation.
	Verbose bool
}

// DefaultOutputOptions returns sensible defaults.
func DefaultOutputOptions() OutputOptions {
	return OutputOptions{
		MaxItemsPerCategory: 10,
	}
}

// String returns a human-readable representation of the diff.
func (d *SysGraphDiff) String() string {
	return d.StringWithOptions(DefaultOutputOptions())
}

// StringWithOptions returns a human-readable representation with custom options.
func (d *SysGraphDiff) StringWithOptions(opts OutputOptions) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("=== Sysgraph Comparison: %s -> %s ===\n\n", d.OldID, d.NewID))

	// Summary line.
	b.WriteString(fmt.Sprintf("Summary: %s\n\n", d.Summary()))

	// Security alerts (always shown, never truncated).
	if len(d.SecurityAlerts) > 0 {
		b.WriteString("--- Security Alerts ---\n")
		for _, alert := range d.SecurityAlerts {
			icon := "[!]"
			if alert.Severity == "critical" {
				icon = "[!!]"
			} else if alert.Severity == "info" {
				icon = "[i]"
			}
			b.WriteString(fmt.Sprintf("%s %s\n", icon, alert.Description))
		}
		b.WriteString("\n")
	}

	// Executables.
	hasExecChanges := len(d.Executables.Added) > 0 || len(d.Executables.Removed) > 0 || len(d.Executables.Matched) > 0
	if hasExecChanges {
		b.WriteString("--- Executables ---\n")
		if len(d.Executables.Matched) > 0 {
			b.WriteString(fmt.Sprintf("Matched (same basename, different path): %d\n", len(d.Executables.Matched)))
			writeExecutableMatches(&b, d.Executables.Matched, opts)
		}
		if len(d.Executables.Added) > 0 {
			b.WriteString(fmt.Sprintf("Truly new (basename not in old): %d\n", len(d.Executables.Added)))
			writeExecutableChanges(&b, "+ ", d.Executables.Added, opts)
		}
		if len(d.Executables.Removed) > 0 {
			b.WriteString(fmt.Sprintf("Truly removed (basename not in new): %d\n", len(d.Executables.Removed)))
			writeExecutableChanges(&b, "- ", d.Executables.Removed, opts)
		}
		b.WriteString("\n")
	}

	// Network.
	if len(d.Network.Added) > 0 || len(d.Network.Removed) > 0 {
		b.WriteString("--- Network Connections ---\n")
		writeNetworkChanges(&b, "+ ", d.Network.Added, opts)
		writeNetworkChanges(&b, "- ", d.Network.Removed, opts)
		b.WriteString("\n")
	}

	// Files.
	if len(d.Files.Added) > 0 || len(d.Files.Removed) > 0 || len(d.Files.Changed) > 0 {
		b.WriteString("--- Files ---\n")
		writeFileChanges(&b, "+ ", d.Files.Added, opts, false)
		writeFileChanges(&b, "- ", d.Files.Removed, opts, false)
		writeFileChanges(&b, "~ ", d.Files.Changed, opts, true)
		b.WriteString("\n")
	}

	// Structure - by basename edges.
	if len(d.Structure.NewEdges) > 0 || len(d.Structure.RemovedEdges) > 0 {
		b.WriteString("--- Process Tree Edges (by basename) ---\n")
		b.WriteString(fmt.Sprintf("Common edges: %d\n", d.Structure.CommonEdgeCount))
		if len(d.Structure.NewEdges) > 0 {
			b.WriteString(fmt.Sprintf("New edges: %d\n", len(d.Structure.NewEdges)))
			writeEdgeChanges(&b, "+ ", d.Structure.NewEdges, opts)
		}
		if len(d.Structure.RemovedEdges) > 0 {
			b.WriteString(fmt.Sprintf("Removed edges: %d\n", len(d.Structure.RemovedEdges)))
			writeEdgeChanges(&b, "- ", d.Structure.RemovedEdges, opts)
		}
		b.WriteString("\n")
	}

	// Normalized counts.
	if len(d.NormalizedCounts) > 0 {
		b.WriteString("--- Normalized (not shown) ---\n")
		for reason, count := range d.NormalizedCounts {
			b.WriteString(fmt.Sprintf("  %d files: %s\n", count, reason))
		}
		b.WriteString("\n")
	}

	if !d.HasChanges() && len(d.Files.Normalized) == 0 {
		b.WriteString("No significant changes detected.\n")
	}

	return b.String()
}

func writeExecutableChanges(b *strings.Builder, prefix string, changes []ExecutableChange, opts OutputOptions) {
	limit := opts.MaxItemsPerCategory
	if opts.Verbose || limit == 0 {
		limit = len(changes)
	}

	for i, change := range changes {
		if i >= limit {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(changes)-limit))
			break
		}
		b.WriteString(fmt.Sprintf("%s%s\n", prefix, change.Path))
		if len(change.Argv) > 0 {
			argStr := strings.Join(change.Argv, " ")
			if len(argStr) > 80 {
				argStr = argStr[:77] + "..."
			}
			b.WriteString(fmt.Sprintf("    Args: %s\n", argStr))
		}
		b.WriteString(fmt.Sprintf("    Action: %d\n", change.ActionID))
	}
}

func writeExecutableMatches(b *strings.Builder, matches []ExecutableMatch, opts OutputOptions) {
	limit := opts.MaxItemsPerCategory
	if opts.Verbose || limit == 0 {
		limit = len(matches)
	}

	for i, match := range matches {
		if i >= limit {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(matches)-limit))
			break
		}
		b.WriteString(fmt.Sprintf("~ %s\n", match.Basename))
		b.WriteString(fmt.Sprintf("    old: %s\n", match.Old.Path))
		b.WriteString(fmt.Sprintf("    new: %s\n", match.New.Path))
	}
}

func writeNetworkChanges(b *strings.Builder, prefix string, changes []NetworkChange, opts OutputOptions) {
	limit := opts.MaxItemsPerCategory
	if opts.Verbose || limit == 0 {
		limit = len(changes)
	}

	for i, change := range changes {
		if i >= limit {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(changes)-limit))
			break
		}
		b.WriteString(fmt.Sprintf("%s%s://%s\n", prefix, change.Protocol, change.Address))
		b.WriteString(fmt.Sprintf("    Action: %d\n", change.ActionID))
	}
}

func writeFileChanges(b *strings.Builder, prefix string, changes []FileChange, opts OutputOptions, showDelta bool) {
	limit := opts.MaxItemsPerCategory
	if opts.Verbose || limit == 0 {
		limit = len(changes)
	}

	// Group by extension for summary when truncating.
	if !opts.Verbose && len(changes) > limit {
		extCounts := make(map[string]int)
		for _, change := range changes[limit:] {
			ext := filepath.Ext(change.Path)
			if ext == "" {
				ext = "(no ext)"
			}
			extCounts[ext]++
		}

		// Show first N items.
		for i := 0; i < limit && i < len(changes); i++ {
			change := changes[i]
			writeFileChange(b, prefix, change, showDelta)
		}

		// Summarize the rest.
		remaining := len(changes) - limit
		b.WriteString(fmt.Sprintf("  ... and %d more (", remaining))
		parts := []string{}
		for ext, count := range extCounts {
			parts = append(parts, fmt.Sprintf("%d %s", count, ext))
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString(")\n")
		return
	}

	for _, change := range changes {
		writeFileChange(b, prefix, change, showDelta)
	}
}

func writeFileChange(b *strings.Builder, prefix string, change FileChange, showDelta bool) {
	b.WriteString(fmt.Sprintf("%s%s\n", prefix, change.Path))
	if showDelta && change.OldDigest.Hash != "" && change.NewDigest.Hash != "" {
		// Show abbreviated digests.
		oldHash := change.OldDigest.Hash
		newHash := change.NewDigest.Hash
		if len(oldHash) > 12 {
			oldHash = oldHash[:12]
		}
		if len(newHash) > 12 {
			newHash = newHash[:12]
		}
		deltaStr := ""
		if change.SizeDelta != 0 {
			if change.SizeDelta > 0 {
				deltaStr = fmt.Sprintf(" (+%d bytes)", change.SizeDelta)
			} else {
				deltaStr = fmt.Sprintf(" (%d bytes)", change.SizeDelta)
			}
		}
		b.WriteString(fmt.Sprintf("    %s.../%d -> %s.../%d%s\n",
			oldHash, change.OldDigest.Size,
			newHash, change.NewDigest.Size,
			deltaStr))
	}
}

func writeSpawnChanges(b *strings.Builder, prefix string, spawns []ProcessSpawn, opts OutputOptions) {
	limit := opts.MaxItemsPerCategory
	if opts.Verbose || limit == 0 {
		limit = len(spawns)
	}

	for i, spawn := range spawns {
		if i >= limit {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(spawns)-limit))
			break
		}
		parentBase := filepath.Base(spawn.ParentExe)
		childBase := filepath.Base(spawn.ChildExe)
		b.WriteString(fmt.Sprintf("%s%s -> %s\n", prefix, parentBase, childBase))
	}
}

func writeEdgeChanges(b *strings.Builder, prefix string, edges []TreeEdge, opts OutputOptions) {
	limit := opts.MaxItemsPerCategory
	if opts.Verbose || limit == 0 {
		limit = len(edges)
	}

	for i, edge := range edges {
		if i >= limit {
			b.WriteString(fmt.Sprintf("  ... and %d more\n", len(edges)-limit))
			break
		}
		b.WriteString(fmt.Sprintf("%s%s -> %s\n", prefix, edge.ParentBasename, edge.ChildBasename))
		// Always show one example with full paths for context.
		if len(edge.Examples) > 0 {
			ex := edge.Examples[0]
			b.WriteString(fmt.Sprintf("    %s\n", ex.ParentExe))
			b.WriteString(fmt.Sprintf("      -> %s\n", ex.ChildExe))
		}
	}
}

// JSON returns a JSON representation of the diff.
func (d *SysGraphDiff) JSON() ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}

// JSONCompact returns a compact JSON representation.
func (d *SysGraphDiff) JSONCompact() ([]byte, error) {
	return json.Marshal(d)
}
