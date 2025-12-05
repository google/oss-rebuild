// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/internal/gitdiff"
	"github.com/pkg/errors"
)

// formatTarListing produces a one-line summary of a tar file entry
// -rw-r--r-- 0 0            5 1970-01-01 00:00:00.000000 file.txt
func formatTarListing(h *tar.Header) string {
	return fmt.Sprintf("%-10s %d %d %12d %-26s %s\n",
		os.FileMode(h.Mode).String(),
		h.Uid,
		h.Gid,
		h.Size,
		h.ModTime.UTC().Format("2006-01-02 15:04:05.000000"),
		h.Name,
	)
}

// compareTar compares two tar archives
func compareTar(ctx compareContext, node *DiffNode, file1, file2 File) (bool, error) {
	// Reset readers
	file1.Reader.Seek(0, io.SeekStart)
	file2.Reader.Seek(0, io.SeekStart)
	tr1 := tar.NewReader(file1.Reader)
	tr2 := tar.NewReader(file2.Reader)
	// First pass: collect entries and record offsets
	// Use slices to support duplicate entries with the same name
	entries1 := make(map[string][]*tarEntry)
	entries2 := make(map[string][]*tarEntry)
	var listing1, listing2 strings.Builder
	// Read all entries from tar1, recording offsets and original order
	var names1 []string
	for {
		hdr, err := tr1.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, errors.Wrap(err, "reading tar1")
		}
		offset, err := file1.Reader.Seek(0, io.SeekCurrent)
		if err != nil {
			return false, errors.Wrap(err, "getting offset in tar1")
		}
		entries1[hdr.Name] = append(entries1[hdr.Name], &tarEntry{
			header:        hdr,
			contentOffset: offset,
		})
		names1 = append(names1, hdr.Name)
	}
	// Read all entries from tar2, recording offsets and original order
	var names2 []string
	for {
		hdr, err := tr2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, errors.Wrap(err, "reading tar2")
		}
		offset, err := file2.Reader.Seek(0, io.SeekCurrent)
		if err != nil {
			return false, errors.Wrap(err, "getting offset in tar2")
		}
		entries2[hdr.Name] = append(entries2[hdr.Name], &tarEntry{
			header:        hdr,
			contentOffset: offset,
		})
		names2 = append(names2, hdr.Name)
	}
	// Pick listing based on whether order is consistent (same relative order for common entries)
	ordersConsistent := checkOrderConsistency(names1, names2)
	if !ordersConsistent {
		sort.Strings(names1)
		sort.Strings(names2)
		node.Comments = append(node.Comments, "Entry order differs (listings shown in sorted order)")
	}
	// Build listings using chosen order
	// Track occurrence index for each name to handle duplicates
	nameIndex1 := make(map[string]int)
	for _, name := range names1 {
		listing1.WriteString(formatTarListing(entries1[name][nameIndex1[name]].header))
		nameIndex1[name]++
	}
	nameIndex2 := make(map[string]int)
	for _, name := range names2 {
		listing2.WriteString(formatTarListing(entries2[name][nameIndex2[name]].header))
		nameIndex2[name]++
	}
	// Compare listings
	match := true
	if listingStr1, listingStr2 := listing1.String(), listing2.String(); listingStr1 != listingStr2 {
		match = false
		listingDiff, err := gitdiff.Strings(listingStr1, listingStr2)
		if err != nil {
			return false, errors.Wrap(err, "diffing tar listings")
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
				var comments []string
				if has1 {
					comments = append(comments, commentOnlyInFirst)
				} else {
					comments = append(comments, commentOnlyInSecond)
				}
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
				// Both have this occurrence - compare based on type
				e1, e2 := list1[i], list2[i]
				if e1.header.Typeflag != e2.header.Typeflag {
					match = false
					node.Details = append(node.Details, DiffNode{
						Source1:  sourceName,
						Source2:  sourceName,
						Comments: []string{fmt.Sprintf("Entry types differ: %c vs %c", e1.header.Typeflag, e2.header.Typeflag)},
					})
					continue
				}
				if !slices.Contains([]byte{tar.TypeReg, tar.TypeRegA}, e1.header.Typeflag) {
					// Skip entries except regular files
					continue
				}
				entryNode := DiffNode{
					Source1: sourceName,
					Source2: sourceName,
				}
				file1.Reader.Seek(e1.contentOffset, io.SeekStart)
				content1 := new(bytes.Buffer)
				if _, err := io.CopyN(content1, file1.Reader, e1.header.Size); err != nil {
					return false, errors.Wrapf(err, "reading content of %s from tar1", sourceName)
				}
				file2.Reader.Seek(e2.contentOffset, io.SeekStart)
				content2 := new(bytes.Buffer)
				if _, err := io.CopyN(content2, file2.Reader, e2.header.Size); err != nil {
					return false, errors.Wrapf(err, "reading content of %s from tar2", sourceName)
				}
				entryFile1 := File{
					Name:   sourceName,
					Reader: bytes.NewReader(content1.Bytes()),
				}
				entryFile2 := File{
					Name:   sourceName,
					Reader: bytes.NewReader(content2.Bytes()),
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

// tarEntry holds a tar header and the archive offset for the start of its content
type tarEntry struct {
	header        *tar.Header
	contentOffset int64
}
