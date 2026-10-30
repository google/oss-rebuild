// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"

	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/genai"
)

// artifactCheck is check_artifact. A match stands until a handoff spends it.
type artifactCheck struct {
	runner  *ScratchRunner
	matched bool
	closest *schema.AgentCandidate // fewest differing entries so far
}

// note keeps c if it is the closest mismatch so far and says which is.
func (k *artifactCheck) note(c schema.AgentCandidate) string {
	if k.closest == nil || c.Entries < k.closest.Entries {
		k.closest = &c
	}
	return fmt.Sprintf("\nClosest so far in this session: %d differing entries (%s).", k.closest.Entries, k.closest.Path)
}

// summaryLines caps the diffr summary check_artifact returns on a mismatch.
const summaryLines = 40

func (k *artifactCheck) tool() *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name: "check_artifact",
			Description: "Compare a candidate artifact against upstream the way the verification does. " +
				"Both are stabilized with tools/stabilize and the sha256 of the stabilized forms compared. " +
				"A mismatch returns the head of diffr's file-level summary with the entry counts " +
				"and names the stabilized forms for tools/diffr. " +
				"Deterministic, no model involved.",
			Parameters: &genai.Schema{
				Type:       genai.TypeObject,
				Properties: map[string]*genai.Schema{"path": {Type: genai.TypeString, Description: "The candidate on the host, such as a file under builds/<build-id>/src/ for one built in the container, or its container path under /src/, which maps to the same file"}},
				Required:   []string{"path"},
			},
			Response: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"result": {Type: genai.TypeString, Description: "MATCH or MISMATCH, with the comparison's details"},
					"match":  {Type: genai.TypeBoolean, Description: "Whether the stabilized forms are equal"},
					"error":  {Type: genai.TypeString, Description: "Why the comparison could not run, if it could not"},
				},
			},
		},
		Function: func(ctx context.Context, args map[string]any) genai.FunctionResponse {
			p, _ := args["path"].(string)
			result, match, err := k.check(ctx, p)
			// An error key marks the call failed, so set it only on an error.
			resp := map[string]any{"result": result, "match": match}
			if err != nil {
				resp["error"] = err.Error()
			}
			return genai.FunctionResponse{Name: "check_artifact", Response: resp}
		},
	}
}

// check compares the candidate at p and words the result for the model.
func (k *artifactCheck) check(ctx context.Context, p string) (result string, match bool, err error) {
	c, err := compareCandidate(ctx, k.runner, p)
	if err != nil {
		return "", false, err
	}
	where := fmt.Sprintf("Stabilized forms: %s and %s. Run tools/diffr on them for content hunks.", c.candidateForm, c.upstreamForm)
	switch {
	case c.match:
		k.matched = true
		return fmt.Sprintf("MATCH: the stabilized candidate equals the stabilized upstream (sha256 %s). "+
			"Call handoff with the commands that produced %s. "+
			"A match is necessary, not sufficient: the job reviews how the bytes were produced, "+
			"in this session's commands and in the handoff, and refuses content that was written, "+
			"copied, or edited into the artifact instead of built.", c.candidate, p), true, nil
	case c.summary == "":
		return fmt.Sprintf("MISMATCH: the stabilized forms differ as bytes (candidate %s, upstream %s) "+
			"but diffr reports no file-level differences. %s", c.candidate, c.upstream, where) + k.note(schema.AgentCandidate{Path: p}), false, nil
	}
	head, lines, shown, entries := summaryHead(c.summary, summaryLines)
	extent := ""
	if lines > shown {
		extent = fmt.Sprintf(", first %d of %d lines", shown, lines)
	}
	return fmt.Sprintf("MISMATCH: stabilized sha256 differ (candidate %s, upstream %s). "+
		"%d differing entries%s:\n%s\n%s", c.candidate, c.upstream, entries, extent, head, where) + k.note(schema.AgentCandidate{Path: p, Entries: entries}), false, nil
}
