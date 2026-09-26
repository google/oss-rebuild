// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"strconv"
	"strings"
)

// Summary renders the diff tree rooted at node as a file-level report for a
// reader who wants to know which entries differ and how, not what changed
// inside them. Each archive is a block opened by "within <name>:" that lists
// its entries grouped as "only in <first>", "only in <second>" and "differ",
// each with a count, then the blocks of its nested archives. Comments other
// than the entry status follow the path in parentheses. An archive whose
// entries all match reports its listing as the difference. Inputs that are
// not archives are one line. Content hunks are omitted.
func (node DiffNode) Summary() string {
	var b lineBuilder
	node = unwrap(node)
	if len(node.Details) == 0 {
		b.WriteLine(node.Source1, " vs ", node.Source2, " differ as a whole", annotate(node.Comments))
		return b.String()
	}
	within(&b, node, node.Source1, node.Source2, "")
	return b.String()
}

// unwrap folds node's decompression layers into it. A decompression layer is
// the same file as its parent, so the parent keeps its names and takes the
// innermost content's comments and entries.
func unwrap(node DiffNode) DiffNode {
	for len(node.Details) == 1 && node.Details[0].Unwrapped {
		inner := node.Details[0]
		node.Comments = append(node.Comments, inner.Comments...)
		node.Details = inner.Details
	}
	return node
}

// within writes node's block: its "within" line, then its entries.
func within(b *lineBuilder, node DiffNode, first, second, indent string) {
	if node.Source1 == node.Source2 {
		b.WriteLine(indent, "within ", node.Source1, ":")
	} else {
		b.WriteLine(indent, "within ", node.Source1, " (vs ", node.Source2, "):")
	}
	entries(b, node.Details, first, second, indent+"  ")
}

// entries writes an archive's differing entries grouped by status, then the
// blocks of its nested archives.
func entries(b *lineBuilder, nodes []DiffNode, first, second, indent string) {
	var groups [3][]string // indexed by NodeStatus
	var listing bool
	var nested []DiffNode
	for _, n := range nodes {
		n = unwrap(n)
		switch {
		case n.Source1 == listingEntry:
			listing = true
		case len(n.Details) == 0:
			groups[n.Status()] = append(groups[n.Status()], n.Source1+annotate(n.Comments))
		default:
			nested = append(nested, n)
		}
	}
	titles := [3]string{StatusBoth: "differ", StatusOnlyFirst: "only in " + first, StatusOnlySecond: "only in " + second}
	var count int
	for _, s := range []NodeStatus{StatusOnlyFirst, StatusOnlySecond, StatusBoth} {
		if len(groups[s]) == 0 {
			continue
		}
		count += len(groups[s])
		b.WriteLine(indent, titles[s], " (", strconv.Itoa(len(groups[s])), "):")
		for _, e := range groups[s] {
			b.WriteLine(indent, "  ", e)
		}
	}
	// The listing differs whenever an entry does, a nested archive included.
	// It is worth a line only when the entries match by name and content and
	// their metadata does not.
	if listing && count == 0 && len(nested) == 0 {
		b.WriteLine(indent, "listing differs (entry metadata only)")
	}
	for _, n := range nested {
		within(b, n, first, second, indent)
	}
}

// annotate renders the comments other than an entry's status, which its group
// already states, as a parenthetical suffix.
func annotate(comments []string) string {
	var kept []string
	for _, c := range comments {
		if c != commentOnlyInFirst && c != commentOnlyInSecond {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return " (" + strings.Join(kept, "; ") + ")"
}
