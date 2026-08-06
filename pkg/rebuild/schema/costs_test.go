// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTokenUsageAdd(t *testing.T) {
	for _, tc := range []struct {
		name  string
		u     TokenUsage
		other TokenUsage
		want  TokenUsage
	}{
		{
			name:  "SameModel",
			u:     TokenUsage{Input: 1, CachedInput: 2, Output: 3, Model: "m"},
			other: TokenUsage{Input: 10, CachedInput: 20, Output: 30, Model: "m"},
			want:  TokenUsage{Input: 11, CachedInput: 22, Output: 33, Model: "m"},
		},
		{
			name:  "UnlabeledReceiverInheritsModel",
			u:     TokenUsage{Input: 1},
			other: TokenUsage{Input: 10, Model: "m"},
			want:  TokenUsage{Input: 11, Model: "m"},
		},
		{
			name:  "UnlabeledOtherKeepsModel",
			u:     TokenUsage{Input: 1, Model: "m"},
			other: TokenUsage{Input: 10},
			want:  TokenUsage{Input: 11, Model: "m"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.u.Add(tc.other)); diff != "" {
				t.Errorf("Add() diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTokenUsageAddAcrossModelsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Add across models did not panic")
		}
	}()
	TokenUsage{Model: "pro"}.Add(TokenUsage{Model: "flash"})
}

func TestTokenUsageSub(t *testing.T) {
	after := TokenUsage{Input: 30, CachedInput: 2, Output: 16, Model: "m"}
	before := TokenUsage{Input: 20, CachedInput: 2, Output: 8, Model: "m"}
	want := TokenUsage{Input: 10, Output: 8, Model: "m"}
	if diff := cmp.Diff(want, after.Sub(before)); diff != "" {
		t.Errorf("Sub() diff (-want +got):\n%s", diff)
	}
}

func TestTokenUsageOrNil(t *testing.T) {
	// A model label alone is not usage worth recording.
	if got := (TokenUsage{Model: "m"}).OrNil(); got != nil {
		t.Errorf("OrNil() of zero counts = %+v, want nil", got)
	}
	if got := (TokenUsage{Output: 1}).OrNil(); got == nil {
		t.Error("OrNil() of non-zero counts = nil, want non-nil")
	}
}
