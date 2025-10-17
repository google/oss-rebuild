// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffr

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/google/oss-rebuild/internal/gitdiff"
	"github.com/pkg/errors"
)

// Options for the Diff function
type Options struct {
	OutputJSON bool // If true, output JSON format; otherwise formatted text diff
}

// File represents an input file with its name and content reader
type File struct {
	Name   string
	Reader io.ReadSeeker
}

// Diff compares two files recursively through archives and returns:
// - match: whether files match byte-for-byte
// - output: diff output in requested format (JSON or text)
// - err: any error encountered
func Diff(file1, file2 File, opts Options) (match bool, output string, err error) {
	// Create root diff node
	rootNode := DiffNode{
		Source1: file1.Name,
		Source2: file2.Name,
	}
	// Compare the files
	match, err = compareFiles(&rootNode, file1, file2)
	if err != nil {
		return false, "", errors.Wrap(err, "comparing files")
	}
	// Generate output
	if opts.OutputJSON {
		data, err := json.MarshalIndent(rootNode, "", "  ")
		if err != nil {
			return match, "", errors.Wrap(err, "marshaling JSON")
		}
		output = string(data)
	} else {
		output = rootNode.String()
	}
	return match, output, nil
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

// compareGzip compares two gzip files
func compareGzip(node *DiffNode, file1, file2 File) (bool, error) {
	// Decompress both files
	gz1, err := gzip.NewReader(file1.Reader)
	if err != nil {
		return false, errors.Wrap(err, "creating gzip reader for file1")
	}
	defer gz1.Close()
	gz2, err := gzip.NewReader(file2.Reader)
	if err != nil {
		return false, errors.Wrap(err, "creating gzip reader for file2")
	}
	defer gz2.Close()
	// Buffer the decompressed content for type detection
	buf1 := new(bytes.Buffer)
	buf2 := new(bytes.Buffer)
	if _, err := io.Copy(buf1, gz1); err != nil {
		return false, errors.Wrap(err, "reading gzip content1")
	}
	if _, err := io.Copy(buf2, gz2); err != nil {
		return false, errors.Wrap(err, "reading gzip content2")
	}
	// Create child node for decompressed content
	childNode := DiffNode{
		Source1: strings.TrimSuffix(file1.Name, ".gz"),
		Source2: strings.TrimSuffix(file2.Name, ".gz"),
	}
	// Compare the decompressed content
	childFile1 := File{
		Name:   childNode.Source1,
		Reader: bytes.NewReader(buf1.Bytes()),
	}
	childFile2 := File{
		Name:   childNode.Source2,
		Reader: bytes.NewReader(buf2.Bytes()),
	}
	match, err := compareFiles(&childNode, childFile1, childFile2)
	if err != nil {
		return false, errors.Wrap(err, "comparing decompressed content")
	}
	// Only add details if there are differences
	if !match {
		node.Details = append(node.Details, childNode)
	}
	return match, nil
}

// compareZip compares two zip archives
func compareZip(node *DiffNode, file1, file2 File) (bool, error) {
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
	if listing1.String() != listing2.String() {
		match = false
		// Add listing diff as a detail
		listingDiff, _ := gitdiff.Strings(listing1.String(), listing2.String())
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
				Source1:  path.Join(file1.Name, name),
				Source2:  path.Join(file2.Name, name),
				Comments: []string{"Entry only in second archive"},
			})
		} else if has1 && !has2 {
			// Entry only in file1
			match = false
			node.Details = append(node.Details, DiffNode{
				Source1:  path.Join(file1.Name, name),
				Source2:  path.Join(file2.Name, name),
				Comments: []string{"Entry only in first archive"},
			})
		} else if has1 && has2 {
			// Entry in both - compare contents
			entryNode := DiffNode{
				Source1: path.Join(file1.Name, name),
				Source2: path.Join(file2.Name, name),
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
	return match, nil
}

// compareTar compares two tar archives
func compareTar(node *DiffNode, file1, file2 File) (bool, error) {
	// Reset readers
	file1.Reader.Seek(0, io.SeekStart)
	file2.Reader.Seek(0, io.SeekStart)
	tr1 := tar.NewReader(file1.Reader)
	tr2 := tar.NewReader(file2.Reader)
	// First pass: collect entries and generate listings
	entries1 := make(map[string]*tarEntry)
	entries2 := make(map[string]*tarEntry)
	var listing1, listing2 strings.Builder
	// Read all entries from tar1
	for {
		hdr, err := tr1.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, errors.Wrap(err, "reading tar1")
		}
		// Buffer the content
		content := new(bytes.Buffer)
		if _, err := io.Copy(content, tr1); err != nil {
			return false, errors.Wrap(err, "reading tar1 entry")
		}
		entries1[hdr.Name] = &tarEntry{
			header:  hdr,
			content: content.Bytes(),
		}
		listing1.WriteString(formatTarListing(hdr))
	}
	// Read all entries from tar2
	for {
		hdr, err := tr2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false, errors.Wrap(err, "reading tar2")
		}
		// Buffer the content
		content := new(bytes.Buffer)
		if _, err := io.Copy(content, tr2); err != nil {
			return false, errors.Wrap(err, "reading tar2 entry")
		}
		entries2[hdr.Name] = &tarEntry{
			header:  hdr,
			content: content.Bytes(),
		}
		listing2.WriteString(formatTarListing(hdr))
	}
	// Compare listings
	match := true
	if listing1.String() != listing2.String() {
		match = false
		listingDiff, err := gitdiff.Strings(listing1.String(), listing2.String())
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
				Source1:  path.Join(file1.Name, name),
				Source2:  path.Join(file2.Name, name),
				Comments: []string{"Entry only in second archive"},
			})
		} else if has1 && !has2 {
			// Entry only in file1
			match = false
			node.Details = append(node.Details, DiffNode{
				Source1:  path.Join(file1.Name, name),
				Source2:  path.Join(file2.Name, name),
				Comments: []string{"Entry only in first archive"},
			})
		} else if has1 && has2 {
			// Entry in both - compare based on type
			if e1.header.Typeflag != e2.header.Typeflag {
				match = false
				node.Details = append(node.Details, DiffNode{
					Source1:  path.Join(file1.Name, name),
					Source2:  path.Join(file2.Name, name),
					Comments: []string{fmt.Sprintf("Entry types differ: %c vs %c", e1.header.Typeflag, e2.header.Typeflag)},
				})
				continue
			}
			// For regular files, compare contents
			if e1.header.Typeflag == tar.TypeReg || e1.header.Typeflag == tar.TypeRegA {
				entryNode := DiffNode{
					Source1: path.Join(file1.Name, name),
					Source2: path.Join(file2.Name, name),
				}
				entryFile1 := File{
					Name:   name,
					Reader: bytes.NewReader(e1.content),
				}
				entryFile2 := File{
					Name:   name,
					Reader: bytes.NewReader(e2.content),
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

// tarEntry holds a tar header and its content
type tarEntry struct {
	header  *tar.Header
	content []byte
}

// readAll reads all content from a ReadSeeker, resetting position first
func readAll(r io.ReadSeeker) ([]byte, error) {
	r.Seek(0, io.SeekStart)
	return io.ReadAll(r)
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
