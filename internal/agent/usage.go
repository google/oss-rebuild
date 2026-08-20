// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/genai"
)

// tokenUsageFromMetadata converts a genai usage-metadata record into a
// schema.TokenUsage labeled with the model that produced it.
func tokenUsageFromMetadata(m *genai.GenerateContentResponseUsageMetadata, model string) schema.TokenUsage {
	if m == nil {
		return schema.TokenUsage{Model: model}
	}
	return schema.TokenUsage{
		Input:       int(m.PromptTokenCount) + int(m.ToolUsePromptTokenCount),
		CachedInput: int(m.CachedContentTokenCount),
		Output:      int(m.CandidatesTokenCount) + int(m.ThoughtsTokenCount),
		Model:       model,
	}
}

// sumTokenUsage sums a slice of genai usage-metadata records (as accumulated by
// llm.Chat.Usage) into a single schema.TokenUsage labeled with model.
func sumTokenUsage(ms []*genai.GenerateContentResponseUsageMetadata, model string) schema.TokenUsage {
	total := schema.TokenUsage{Model: model}
	for _, m := range ms {
		total = total.Add(tokenUsageFromMetadata(m, model))
	}
	return total
}
