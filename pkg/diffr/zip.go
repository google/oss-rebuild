// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"slices"
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
func compareZip(ctx compareContext, node *DiffNode, file1, file2 File) (bool, error) {
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
	// Create maps for entries and track original order
	// Use slices to support duplicate entries with the same name
	entries1 := make(map[string][]*zip.File)
	entries2 := make(map[string][]*zip.File)
	var names1, names2 []string
	for _, f := range zr1.File {
		entries1[f.Name] = append(entries1[f.Name], f)
		names1 = append(names1, f.Name)
	}
	for _, f := range zr2.File {
		entries2[f.Name] = append(entries2[f.Name], f)
		names2 = append(names2, f.Name)
	}
	// Pick listing based on whether order is consistent (same relative order for common entries)
	ordersConsistent := checkOrderConsistency(names1, names2)
	if !ordersConsistent {
		sort.Strings(names1)
		sort.Strings(names2)
		node.Comments = append(node.Comments, "Entry order differs (listings shown in sorted order)")
	}
	// Generate file listings using chosen order
	// Track occurrence index for each name to handle duplicates
	var listing1, listing2 strings.Builder
	nameIndex1 := make(map[string]int)
	for _, name := range names1 {
		idx := nameIndex1[name]
		listing1.WriteString(formatZipListing(&entries1[name][idx].FileHeader))
		nameIndex1[name]++
	}
	nameIndex2 := make(map[string]int)
	for _, name := range names2 {
		idx := nameIndex2[name]
		listing2.WriteString(formatZipListing(&entries2[name][idx].FileHeader))
		nameIndex2[name]++
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
	// Second pass: compare entries, handling duplicates by position
	var iterationOrder []string
	seen := make(map[string]bool)
	for _, name := range slices.Concat(names1, names2) {
		if !seen[name] {
			iterationOrder = append(iterationOrder, name)
			seen[name] = true
		}
	}
	for _, name := range iterationOrder {
		list1, list2 := entries1[name], entries2[name]
		count1, count2 := len(list1), len(list2)
		maxCount := max(count2, count1)
		// Iterate through all occurrences (positional matching)
		for i := range maxCount {
			// Generate source names with occurrence numbers if there are duplicates
			sourceName := name
			if maxCount > 1 {
				sourceName = fmt.Sprintf("%s [occurrence %d]", name, i+1)
			}
			has1, has2 := i < count1, i < count2
			if has1 != has2 {
				// Extra occurrence
				match = false
				comments := []string{map[bool]string{true: commentOnlyInFirst, false: commentOnlyInSecond}[has1]}
				// Only add duplicate comment if this is actually a duplicate scenario
				if (has1 && count1 > 1) || (has2 && count2 > 1) {
					comments = append(comments, "Unmatched duplicate entry")
				}
				node.Details = append(node.Details, DiffNode{
					Source1:  sourceName,
					Source2:  sourceName,
					Comments: comments,
				})
			} else if has1 && has2 {
				// Both have this occurrence - compare contents
				e1, e2 := list1[i], list2[i]
				entryNode := DiffNode{
					Source1: sourceName,
					Source2: sourceName,
				}
				// Open and compare entry contents
				r1, err := e1.Open()
				if err != nil {
					return false, errors.Wrapf(err, "opening %s in file1", sourceName)
				}
				defer r1.Close()
				r2, err := e2.Open()
				if err != nil {
					return false, errors.Wrapf(err, "opening %s in file2", sourceName)
				}
				defer r2.Close()
				// Buffer for comparison
				buf1 := new(bytes.Buffer)
				buf2 := new(bytes.Buffer)
				io.Copy(buf1, r1)
				io.Copy(buf2, r2)
				entryFile1 := File{
					Name:   sourceName,
					Reader: bytes.NewReader(buf1.Bytes()),
				}
				entryFile2 := File{
					Name:   sourceName,
					Reader: bytes.NewReader(buf2.Bytes()),
				}
				entryMatch, err := compareFiles(ctx.Child(), &entryNode, entryFile1, entryFile2)
				if err != nil {
					return false, errors.Wrapf(err, "comparing %s", sourceName)
				}
				if !entryMatch {
					match = false
					node.Details = append(node.Details, entryNode)
				}
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
