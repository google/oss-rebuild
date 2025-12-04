// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/pkg/errors"
)

// Options for the Diff function
type Options struct {
	Output     io.Writer // If non-nil, write formatted text diff here
	OutputJSON io.Writer // If non-nil, write JSON diff here
	OutputNode *DiffNode // If non-nil, populated with the diff tree structure
	MaxDepth   int       // Maximum archive nesting depth to recurse into (0 = unlimited)
}

// compareContext holds options and state for the comparison
type compareContext struct {
	context.Context
	maxDepth int
	depth    int
}

func (c compareContext) Child() compareContext {
	c.depth++
	return c
}

// File represents an input file with its name and content reader
type File struct {
	Name   string
	Reader io.ReadSeeker
}

var ErrNoDiff = errors.New("no diff found")

// Diff compares two files recursively through archives
// If no diff is found, ErrNoDiff is returned
func Diff(ctx context.Context, file1, file2 File, opts Options) error {
	// Create root diff node
	rootNode := DiffNode{
		Source1: file1.Name,
		Source2: file2.Name,
	}
	// Create context for comparison
	cmpCtx := compareContext{Context: ctx, maxDepth: opts.MaxDepth}
	// Compare the files
	match, err := compareFiles(cmpCtx, &rootNode, file1, file2)
	if err != nil {
		return errors.Wrap(err, "comparing files")
	}
	if match {
		return ErrNoDiff
	}
	// Populate OutputNode if requested
	if opts.OutputNode != nil {
		*opts.OutputNode = rootNode
	}
	// Generate JSON output if requested
	if opts.OutputJSON != nil {
		enc := json.NewEncoder(opts.OutputJSON)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rootNode); err != nil {
			return errors.Wrap(err, "marshaling JSON")
		}
	}
	// Generate text output if requested
	if opts.Output != nil {
		_, err := io.WriteString(opts.Output, rootNode.String())
		if err != nil {
			return errors.Wrap(err, "writing diff")
		}
	}
	return nil
}

// compareFiles compares two files and populates the DiffNode
func compareFiles(ctx compareContext, node *DiffNode, file1, file2 File) (bool, error) {
	// First, use compareBinary to perform byte-level comparison.
	// This catches any differences that type-aware differs might miss due to
	// parser canonicalization or lacking semantic reporting.
	match, err := compareBinary(node, file1, file2)
	if err != nil {
		return false, err
	}
	if match {
		return true, nil // Files are byte-identical
	}
	// Files differ. Try to improve reporting with type-aware diff
	type1, err := DetectFileType(file1.Reader)
	if err != nil {
		return false, errors.Wrap(err, "detecting type of file1")
	}
	type2, err := DetectFileType(file2.Reader)
	if err != nil {
		return false, errors.Wrap(err, "detecting type of file2")
	}
	// If types don't match, just note that difference
	if type1 != type2 {
		node.Comments = []string{fmt.Sprintf("File types differ: %s vs %s", type1, type2)}
		return false, nil
	}
	// Check if we've reached max depth for archive types
	// MaxDepth of 0 means unlimited
	if ctx.maxDepth > 0 && ctx.depth >= ctx.maxDepth {
		switch type1 {
		case TypeGzip, TypeZip, TypeTar:
			// Don't recurse into archives at max depth, add comment and report binary diff
			node.Comments = append(node.Comments, fmt.Sprintf("Archive not expanded (depth limit %d reached)", ctx.maxDepth))
			return false, nil
		}
	}
	// Create a temporary node to collect type-aware differ output
	typedNode := DiffNode{
		Source1: node.Source1,
		Source2: node.Source2,
	}
	switch type1 {
	case TypeGzip:
		match, err = compareGzip(ctx, &typedNode, file1, file2)
	case TypeZip:
		match, err = compareZip(ctx, &typedNode, file1, file2)
	case TypeTar:
		match, err = compareTar(ctx, &typedNode, file1, file2)
	case TypeText:
		match, err = compareText(&typedNode, file1, file2)
	case TypeBinary:
		return false, nil // compareBinary already called
	default:
		return false, fmt.Errorf("unknown file type: %v", type1)
	}
	if err != nil {
		return false, err
	}
	// If the typed differ generated output, use it instead of the binary comparison
	if typedNode.UnifiedDiff != nil || len(typedNode.Comments) > 0 || len(typedNode.Details) > 0 {
		node.UnifiedDiff = typedNode.UnifiedDiff
		node.Comments = typedNode.Comments
		node.Details = typedNode.Details
	} else {
		// Typed differ generated no diffs but bytes don't match
		node.Comments = []string{"Bytes differ but no semantic diff generated"}
	}
	return false, nil
}

// compareBinary compares two binary files
func compareBinary(node *DiffNode, file1, file2 File) (bool, error) {
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
	// TODO: Produce an actual diff for binary files.
	node.Comments = append(node.Comments, "Binary files differ")
	return false, nil
}

// readAll reads all content from a ReadSeeker, resetting position first
func readAll(r io.ReadSeeker) ([]byte, error) {
	r.Seek(0, io.SeekStart)
	return io.ReadAll(r)
}
