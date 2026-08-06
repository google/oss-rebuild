// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/genai"
)

func TestTokenUsageFromMetadata(t *testing.T) {
	got := tokenUsageFromMetadata(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:        11,
		ToolUsePromptTokenCount: 5,
		CandidatesTokenCount:    22,
		ThoughtsTokenCount:      33,
		CachedContentTokenCount: 4,
	}, "gemini-2.5-pro")
	want := schema.TokenUsage{
		Input:       16,
		CachedInput: 4,
		Output:      55,
		Model:       "gemini-2.5-pro",
	}
	if got != want {
		t.Errorf("tokenUsageFromMetadata = %+v, want %+v", got, want)
	}
	// A nil record still carries the model label with zero counts.
	if got := tokenUsageFromMetadata(nil, "m"); got != (schema.TokenUsage{Model: "m"}) {
		t.Errorf("tokenUsageFromMetadata(nil) = %+v, want {Model: m}", got)
	}
}

func TestSumTokenUsage(t *testing.T) {
	got := sumTokenUsage([]*genai.GenerateContentResponseUsageMetadata{
		{PromptTokenCount: 1, CandidatesTokenCount: 2},
		{PromptTokenCount: 4, CandidatesTokenCount: 5, ThoughtsTokenCount: 3},
		nil,
	}, "m")
	want := schema.TokenUsage{Input: 5, Output: 10, Model: "m"}
	if got != want {
		t.Errorf("sumTokenUsage = %+v, want %+v", got, want)
	}
	// An empty slice sums to a zero-count (nil-worthy) usage.
	if got := sumTokenUsage(nil, "m"); got.OrNil() != nil {
		t.Errorf("sumTokenUsage(nil) = %+v, want zero counts", got)
	}
}
