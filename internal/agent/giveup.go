// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"cmp"
	"context"
	"strings"

	"github.com/google/oss-rebuild/pkg/llm"
	"google.golang.org/genai"
)

// gaveUp ends the session, not the iteration. Another proposal would only vary
// a build the model has shown cannot match.
type gaveUp struct{ Reason string }

func (g gaveUp) Error() string { return "the model gave up: " + g.Reason }

// giveUpTool hands the model's give_up to record. The session reads it when
// the turn ends.
func giveUpTool(record func(reason string)) *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "give_up",
			Description: "Declare that the artifact cannot be produced from the source at the ref, and end the session. Only with the evidence in hand: content generated from a resource that no longer exists, a release cut from a tree that was never published, a toolchain that cannot be obtained. A difference you have not explained is not evidence.",
			Parameters: &genai.Schema{
				Type:       genai.TypeObject,
				Properties: map[string]*genai.Schema{"reason": {Type: genai.TypeString, Description: "Two or three sentences: what the artifact holds that the source cannot produce, and how you know"}},
				Required:   []string{"reason"},
			},
			Response: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"result": {Type: genai.TypeString}}},
		},
		Function: func(_ context.Context, args map[string]any) genai.FunctionResponse {
			reason, _ := args["reason"].(string)
			record(cmp.Or(strings.TrimSpace(reason), "no reason given"))
			return genai.FunctionResponse{Name: "give_up", Response: map[string]any{"result": "Recorded. End the turn now."}}
		},
	}
}
