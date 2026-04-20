// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"regexp"
	"testing"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestApplySuccessRegex(t *testing.T) {
	tests := []struct {
		name         string
		successRegex *regexp.Regexp
		rebuilds     []rundex.Rebuild
		wantSuccess  []bool
	}{
		{
			name:         "no regex",
			successRegex: nil,
			rebuilds: []rundex.Rebuild{
				{RebuildAttempt: schema.RebuildAttempt{Success: false, Message: "failed"}},
			},
			wantSuccess: []bool{false},
		},
		{
			name:         "matching regex",
			successRegex: regexp.MustCompile("expected failure"),
			rebuilds: []rundex.Rebuild{
				{RebuildAttempt: schema.RebuildAttempt{Success: false, Message: "this is an expected failure"}},
			},
			wantSuccess: []bool{true},
		},
		{
			name:         "non-matching regex",
			successRegex: regexp.MustCompile("expected failure"),
			rebuilds: []rundex.Rebuild{
				{RebuildAttempt: schema.RebuildAttempt{Success: false, Message: "actual failure"}},
			},
			wantSuccess: []bool{false},
		},
		{
			name:         "already successful",
			successRegex: regexp.MustCompile(".*"),
			rebuilds: []rundex.Rebuild{
				{RebuildAttempt: schema.RebuildAttempt{Success: true, Message: ""}},
			},
			wantSuccess: []bool{true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &Deps{SuccessRegex: tt.successRegex}
			applySuccessRegex(deps.SuccessRegex, tt.rebuilds)
			for i, rb := range tt.rebuilds {
				if rb.Success != tt.wantSuccess[i] {
					t.Errorf("rebuild %d success = %v, want %v", i, rb.Success, tt.wantSuccess[i])
				}
			}
		})
	}
}
