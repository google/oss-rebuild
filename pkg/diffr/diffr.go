// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/oss-rebuild/internal/gitdiff"
	"github.com/pkg/errors"
)

// Options for the Diff function
type Options struct {
	Output     io.Writer
	OutputJSON bool // If true, output JSON format; otherwise formatted text diff
}

// File represents an input file with its name and content reader
type File struct {
	Name   string
	Reader io.ReadSeeker
}

var ErrNoDiff = errors.New("no diff found")

// Diff compares two files recursively through archives
// If no diff is found, ErrNoDiff is returned
func Diff(file1, file2 File, opts Options) error {
	// Create root diff node
	rootNode := DiffNode{
		Source1: file1.Name,
		Source2: file2.Name,
	}
	// Compare the files
	match, err := compareFiles(&rootNode, file1, file2)
	if err != nil {
		return errors.Wrap(err, "comparing files")
	}
	if match {
		return ErrNoDiff
	}
	// Generate output only if configured
	if opts.Output != nil {
		if opts.OutputJSON {
			enc := json.NewEncoder(opts.Output)
			enc.SetIndent("", "  ")
			if err := enc.Encode(rootNode); err != nil {
				return errors.Wrap(err, "marshaling JSON")
			}
		} else {
			_, err := io.WriteString(opts.Output, rootNode.String())
			if err != nil {
				return errors.Wrap(err, "writing diff")
			}
		}
	}
	return nil
}

// compareFiles compares two files and populates the DiffNode
func compareFiles(node *DiffNode, file1, file2 File) (bool, error) {
	// Detect file types
	type1, err := DetectFileType(file1.Reader)
	if err != nil {
		return false, errors.Wrap(err, "detecting type of file1")
	}
	type2, err := DetectFileType(file2.Reader)
	if err != nil {
		return false, errors.Wrap(err, "detecting type of file2")
	}
	// If types don't match, just note the difference
	if type1 != type2 {
		node.Comments = append(node.Comments, fmt.Sprintf("File types differ: %s vs %s", type1, type2))
		return false, nil
	}
	// Handle based on file type
	switch type1 {
	case TypeGzip:
		return compareGzip(node, file1, file2)
	case TypeZip:
		return compareZip(node, file1, file2)
	case TypeTar:
		return compareTar(node, file1, file2)
	case TypeText:
		return compareText(node, file1, file2)
	case TypeBinary:
		return compareBinary(node, file1, file2)
	default:
		return false, fmt.Errorf("unknown file type: %v", type1)
	}
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
	// Generate unified diff using the gitdiff package
	diff, err := gitdiff.Strings(string(content1), string(content2))
	if err != nil {
		return false, errors.Wrap(err, "generating diff")
	}
	if diff != "" {
		node.UnifiedDiff = &diff
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
