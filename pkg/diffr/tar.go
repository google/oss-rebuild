// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
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
func compareTar(node *DiffNode, file1, file2 File) (bool, error) {
	// Reset readers
	file1.Reader.Seek(0, io.SeekStart)
	file2.Reader.Seek(0, io.SeekStart)
	tr1 := tar.NewReader(file1.Reader)
	tr2 := tar.NewReader(file2.Reader)
	// First pass: collect entries and record offsets
	entries1 := make(map[string]*tarEntry)
	entries2 := make(map[string]*tarEntry)
	var listing1, listing2 strings.Builder
	// Read all entries from tar1, recording offsets
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
		entries1[hdr.Name] = &tarEntry{
			header:        hdr,
			contentOffset: offset,
		}
		listing1.WriteString(formatTarListing(hdr))
	}
	// Read all entries from tar2, recording offsets
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
		entries2[hdr.Name] = &tarEntry{
			header:        hdr,
			contentOffset: offset,
		}
		listing2.WriteString(formatTarListing(hdr))
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
	// Second pass: compare entries
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
			// Entry in both - compare based on type
			if e1.header.Typeflag != e2.header.Typeflag {
				match = false
				node.Details = append(node.Details, DiffNode{
					Source1:  name,
					Source2:  name,
					Comments: []string{fmt.Sprintf("Entry types differ: %c vs %c", e1.header.Typeflag, e2.header.Typeflag)},
				})
				continue
			}
			// For regular files, compare contents
			if e1.header.Typeflag == tar.TypeReg || e1.header.Typeflag == tar.TypeRegA {
				entryNode := DiffNode{
					Source1: name,
					Source2: name,
				}
				file1.Reader.Seek(e1.contentOffset, io.SeekStart)
				content1 := new(bytes.Buffer)
				if _, err := io.CopyN(content1, file1.Reader, e1.header.Size); err != nil {
					return false, errors.Wrapf(err, "reading content of %s from tar1", name)
				}
				file2.Reader.Seek(e2.contentOffset, io.SeekStart)
				content2 := new(bytes.Buffer)
				if _, err := io.CopyN(content2, file2.Reader, e2.header.Size); err != nil {
					return false, errors.Wrapf(err, "reading content of %s from tar2", name)
				}
				entryFile1 := File{
					Name:   name,
					Reader: bytes.NewReader(content1.Bytes()),
				}
				entryFile2 := File{
					Name:   name,
					Reader: bytes.NewReader(content2.Bytes()),
				}
				entryMatch, err := compareFiles(&entryNode, entryFile1, entryFile2)
				if err != nil {
					return false, errors.Wrapf(err, "comparing %s", name)
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
