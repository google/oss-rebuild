// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

// StabilizeOpts aggregates stabilizers to be used in stabilization.
type StabilizeOpts struct {
	Stabilizers []any
}

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
