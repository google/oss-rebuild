// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/internal/gitdiff"
	"github.com/pkg/errors"
)

// formatMethod maps common zip methods to strings.
func formatMethod(method uint16) string {
	switch method {
	case zip.Store:
		return "Store"
	case zip.Deflate:
		return "Deflate"
	default:
		// Fallback to hex for other/unknown methods
		return fmt.Sprintf("0x%04X", method)
	}
}

// formatZipListing produces a one-line summary of a zip file entry
// -rw-r--r-- Deflate 4          1980-01-01 00:00:00.000000 foo.txt
func formatZipListing(f *zip.FileHeader) string {
	return fmt.Sprintf("%-10s %-8s %-12d %-26s %s\n",
		f.Mode().String(),
		formatMethod(f.Method),
		f.UncompressedSize64,
		f.Modified.UTC().Format("2006-01-02 15:04:05.000000"),
		f.Name,
	)
}

// compareZip compares two zip archives
func compareZip(ctx context.Context, node *DiffNode, file1, file2 File) (bool, error) {
	// Get file sizes
	size1, err := getSize(file1.Reader)
	if err != nil {
		return false, errors.Wrap(err, "getting size of file1")
	}
	size2, err := getSize(file2.Reader)
	if err != nil {
		return false, errors.Wrap(err, "getting size of file2")
	}
	// Open both zip files
	zr1, err := zip.NewReader(file1.Reader.(io.ReaderAt), size1)
	if err != nil {
		return false, errors.Wrap(err, "opening zip file1")
	}
	zr2, err := zip.NewReader(file2.Reader.(io.ReaderAt), size2)
	if err != nil {
		return false, errors.Wrap(err, "opening zip file2")
	}
	// Create maps for entries
	entries1 := make(map[string]*zip.File)
	entries2 := make(map[string]*zip.File)
	// Generate file listings
	var listing1, listing2 strings.Builder
	for _, f := range zr1.File {
		entries1[f.Name] = f
		listing1.WriteString(formatZipListing(&f.FileHeader))
	}
	for _, f := range zr2.File {
		entries2[f.Name] = f
		listing2.WriteString(formatZipListing(&f.FileHeader))
	}
	// Compare listings
	match := true
	if listingStr1, listingStr2 := listing1.String(), listing2.String(); listingStr1 != listingStr2 {
		match = false
		listingDiff, err := gitdiff.Strings(listingStr1, listingStr2)
		if err != nil {
			return false, errors.Wrap(err, "diffing zip listings")
		}
		if listingDiff != "" {
			listingNode := DiffNode{
				Source1:     "file list",
				Source2:     "file list",
				UnifiedDiff: &listingDiff,
			}
			node.Details = append(node.Details, listingNode)
		}
	}
	// Get all unique entry names
	allNames := make(map[string]bool)
	for name := range entries1 {
		allNames[name] = true
	}
	for name := range entries2 {
		allNames[name] = true
	}
	// Sort names for consistent ordering
	var sortedNames []string
	for name := range allNames {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	// Compare individual entries
	for _, name := range sortedNames {
		e1, has1 := entries1[name]
		e2, has2 := entries2[name]
		if !has1 && has2 {
			// Entry only in file2
			match = false
			node.Details = append(node.Details, DiffNode{
				Source1:  name,
				Source2:  name,
				Comments: []string{"Entry only in second archive"},
			})
		} else if has1 && !has2 {
			// Entry only in file1
			match = false
			node.Details = append(node.Details, DiffNode{
				Source1:  name,
				Source2:  name,
				Comments: []string{"Entry only in first archive"},
			})
		} else if has1 && has2 {
			// Entry in both - compare contents
			entryNode := DiffNode{
				Source1: name,
				Source2: name,
			}
			// Open and compare entry contents
			r1, err := e1.Open()
			if err != nil {
				return false, errors.Wrapf(err, "opening %s in file1", name)
			}
			defer r1.Close()
			r2, err := e2.Open()
			if err != nil {
				return false, errors.Wrapf(err, "opening %s in file2", name)
			}
			defer r2.Close()
			// Buffer for comparison
			buf1 := new(bytes.Buffer)
			buf2 := new(bytes.Buffer)
			io.Copy(buf1, r1)
			io.Copy(buf2, r2)
			entryFile1 := File{
				Name:   name,
				Reader: bytes.NewReader(buf1.Bytes()),
			}
			entryFile2 := File{
				Name:   name,
				Reader: bytes.NewReader(buf2.Bytes()),
			}
			entryMatch, err := compareFiles(ctx, &entryNode, entryFile1, entryFile2)
			if err != nil {
				return false, errors.Wrapf(err, "comparing %s", name)
			}
			if !entryMatch {
				match = false
				node.Details = append(node.Details, entryNode)
			}
		}
	}
	return match, nil
}

// getSize determines the size of a ReadSeeker
func getSize(r io.ReadSeeker) (int64, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = r.Seek(0, io.SeekStart)
	return size, err
}
