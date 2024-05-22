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

package archive

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestContentSummary_Diff(t *testing.T) {
	tests := []struct {
		name      string
		left      *ContentSummary
		right     *ContentSummary
		wantLeft  []string
		wantDiffs []string
		wantRight []string
	}{
		{
			name:  "Empty Summaries",
			left:  &ContentSummary{},
			right: &ContentSummary{},
		},
		{
			name: "Identical Summaries",
			left: &ContentSummary{
				Files:      []string{"file1", "file2"},
				FileHashes: []string{"hash1", "hash2"},
			},
			right: &ContentSummary{
				Files:      []string{"file1", "file2"},
				FileHashes: []string{"hash1", "hash2"},
			},
		},
		{
			name: "Left Only Files",
			left: &ContentSummary{
				Files:      []string{"file1", "file2"},
				FileHashes: []string{"hash1", "hash2"},
			},
			right:    &ContentSummary{},
			wantLeft: []string{"file1", "file2"},
		},
		{
			name: "Right Only Files",
			left: &ContentSummary{},
			right: &ContentSummary{
				Files:      []string{"file3", "file4"},
				FileHashes: []string{"hash3", "hash4"},
			},
			wantRight: []string{"file3", "file4"},
		},
		{
			name: "Files with Different Hashes",
			left: &ContentSummary{
				Files:      []string{"file1", "file2"},
				FileHashes: []string{"hash1", "hash2"},
			},
			right: &ContentSummary{
				Files:      []string{"file1", "file2"},
				FileHashes: []string{"hash1", "different_hash2"},
			},
			wantDiffs: []string{"file2"},
		},
		{
			name: "Overlap",
			left: &ContentSummary{
				Files:      []string{"file1", "file2", "file3"},
				FileHashes: []string{"hash1", "hash2", "hash3"},
			},
			right: &ContentSummary{
				Files:      []string{"file2", "file4", "file5"},
				FileHashes: []string{"different_hash2", "hash4", "hash5"},
			},
			wantLeft:  []string{"file1", "file3"},
			wantDiffs: []string{"file2"},
			wantRight: []string{"file4", "file5"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLeft, gotDiffs, gotRight := tt.left.Diff(tt.right)

			if diff := cmp.Diff(tt.wantLeft, gotLeft); diff != "" {
				t.Errorf("leftOnly mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantDiffs, gotDiffs); diff != "" {
				t.Errorf("diffs mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantRight, gotRight); diff != "" {
				t.Errorf("rightOnly mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
