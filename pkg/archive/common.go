// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package archive provides common types and functions for archive processing.
package archive

// Format represents the archive types of packages.
type Format int

// ArchiveType constants specify the type of archive of a file/target.
const (
	UnknownFormat Format = iota
	TarGzFormat
	TarFormat
	ZipFormat
	RawFormat
)

// ContentSummary is a summary of rebuild-relevant features of an archive.
type ContentSummary struct {
	Files      []string
	FileHashes []string
	CRLFCount  int
}

// Diff returns the files that are only in this summary, the files that are in both summaries but have different hashes, and the files that are only in the other summary.
func (cs *ContentSummary) Diff(other *ContentSummary) (leftOnly, diffs, rightOnly []string) {
	left := cs
	right := other
	var i, j int
	for i < len(left.Files) || j < len(right.Files) {
		switch {
		case i >= len(left.Files):
			rightOnly = append(rightOnly, right.Files[j])
			j++
		case j >= len(right.Files):
			leftOnly = append(leftOnly, left.Files[i])
			i++
		case left.Files[i] == right.Files[j]:
			if left.FileHashes[i] != right.FileHashes[j] {
				diffs = append(diffs, right.Files[j])
			}
			i++
			j++
		case left.Files[i] < right.Files[j]:
			leftOnly = append(leftOnly, left.Files[i])
			i++
		case left.Files[i] > right.Files[j]:
			rightOnly = append(rightOnly, right.Files[j])
			j++
		}
	}
	return
}
