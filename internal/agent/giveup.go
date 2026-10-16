// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"strings"

	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/genai"
)

// gaveUp ends the session, not the iteration. Another proposal would only vary
// a build the model has shown cannot match.
type gaveUp struct {
	Reason  string
	Closest *schema.AgentCandidate // the job's closest check, if any
}

func (g gaveUp) Error() string { return "the model gave up: " + g.Reason }

// giveUpArgs are give_up's arguments.
type giveUpArgs struct {
	Reason        string        `json:"reason"`
	Closest       string        `json:"closest,omitempty"`
	BuildCommands buildCommands `json:"build_commands,omitzero"`
	Remaining     string        `json:"remaining,omitempty"`
}

// giveUpTool hands the model's give_up to record. The session reads it when
// the turn ends.
func giveUpTool(record func(giveUpArgs)) *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "give_up",
			Description: "End the session with the closest build you reached, when you cannot get closer to upstream. The job files the closest candidate, its commands, and what still differs for a person to finish.",
			Parameters: &genai.Schema{
				Type:     genai.TypeObject,
				Required: []string{"reason"},
				Properties: map[string]*genai.Schema{
					"reason":  {Type: genai.TypeString, Description: "Two or three sentences: why you cannot get closer, and how you know"},
					"closest": {Type: genai.TypeString, Description: "The path of the closest candidate check_artifact compared, if any"},
					"build_commands": {
						Type:        genai.TypeObject,
						Description: "The commands that produced the closest candidate, split as for handoff",
						Properties: map[string]*genai.Schema{
							"deps":  {Type: genai.TypeString, Description: "Every command that installs or fetches anything"},
							"build": {Type: genai.TypeString, Description: "The rest: what produced the candidate"},
						},
					},
					"remaining": {Type: genai.TypeString, Description: "Each difference that remains, and what you tried against it"},
				},
			},
			Response: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"result": {Type: genai.TypeString}}},
		},
		Function: func(_ context.Context, args map[string]any) genai.FunctionResponse {
			var g giveUpArgs
			if b, err := json.Marshal(args); err == nil {
				json.Unmarshal(b, &g)
			}
			g.Reason = cmp.Or(strings.TrimSpace(g.Reason), "no reason given")
			record(g)
			return genai.FunctionResponse{Name: "give_up", Response: map[string]any{"result": "Recorded. End the turn now."}}
		},
	}
}
