// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package buildpolicy judges an agent-written build script against the content
// rule: the build tooling on the source at the ref makes every artifact byte.
package buildpolicy

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/google/oss-rebuild/pkg/llm"
	"google.golang.org/genai"
)

// Rule ids, cited by review violations and the agent's refusals.
const (
	RuleTranscribe = "F1" // literal written into a shipped file
	RuleCopy       = "F2" // copied from upstream or another ref
	RuleArchive    = "F3" // output archive repacked by hand
	RuleEdit       = "F4" // a shipped file's content edited
	RuleDelete     = "F5" // shipped file dropped to hide a diff
)

var ruleTitles = map[string]string{
	RuleTranscribe: "writing content into a file that ships in the artifact",
	RuleCopy:       "taking content from the published artifact or from another commit",
	RuleArchive:    "building, unpacking, or editing the output archive by hand",
	RuleEdit:       "editing the content of a file that ships in the artifact",
	RuleDelete:     "deleting a shipped file to hide a difference",
}

// Title returns the one-line statement of a rule.
func Title(rule string) string { return ruleTitles[rule] }

// Target is the reviewed package version and the source it builds from.
type Target struct {
	Package string
	Version string
	Ref     string
}

// ReviewInput is what the reviewer sees, all of it known to the agent.
type ReviewInput struct {
	Target                       Target
	Ecosystem                    string
	Artifact                     string
	Deps, Build                  string // the proposal, rendered
	PreviousBuild, PreviousError string // prior attempt's script and error
	Transcript                   string // a handoff session's commands
}

// Verdicts.
const (
	VerdictAllow   = "allow"
	VerdictDeclare = "declare"
	VerdictReject  = "reject"
)

// Violation is one rule the reviewer found broken.
type Violation struct {
	Rule string `json:"rule"`
	Line string `json:"line"` // quoted verbatim from the proposal
	Why  string `json:"why"`
}

// Declaration is an allowed transform the script must state.
type Declaration struct {
	Kind      string   `json:"kind"`
	Paths     []string `json:"paths"`
	Mechanism string   `json:"mechanism"`
}

// Review is the reviewer's verdict on one proposal.
type Review struct {
	Verdict      string        `json:"verdict"`
	Violations   []Violation   `json:"violations"`
	Declarations []Declaration `json:"declarations"`
	Rationale    string        `json:"rationale"`
}

// Rejected reports whether the review refuses the proposal.
func (r *Review) Rejected() bool { return r != nil && r.Verdict == VerdictReject }

// Message renders a rejecting review as the refusal the model reads.
func (r *Review) Message() string {
	var b strings.Builder
	b.WriteString("proposal refused: the script supplies artifact content instead of building it\n")
	for _, v := range r.Violations {
		fmt.Fprintf(&b, "\n%s %s\n  %s\n  %s\n", v.Rule, ruleTitles[v.Rule], v.Line, v.Why)
	}
	if r.Rationale != "" {
		fmt.Fprintf(&b, "\n%s\n", r.Rationale)
	}
	b.WriteString("\nRemove the refused commands and propose again, or say in your diagnosis that the package cannot be built from the source at this ref and stop. A refused proposal is not counted as a build.")
	return b.String()
}

var reviewSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"verdict": {
			Type:        genai.TypeString,
			Enum:        []string{VerdictAllow, VerdictDeclare, VerdictReject},
			Description: "reject when the script breaks any of F1 to F5; declare when it only makes transforms D1 to D5, which must be stated; allow otherwise",
		},
		"violations": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"rule": {Type: genai.TypeString, Enum: []string{RuleTranscribe, RuleCopy, RuleArchive, RuleEdit, RuleDelete}},
					"line": {Type: genai.TypeString, Description: "the offending script line, quoted verbatim from the proposal"},
					"why":  {Type: genai.TypeString, Description: "one sentence naming the content supplied or the shipped path changed"},
				},
				Required: []string{"rule", "line", "why"},
			},
			Description: "empty unless the verdict is reject",
		},
		"declarations": {
			Type: genai.TypeArray,
			Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"kind":      {Type: genai.TypeString, Enum: []string{"D1", "D2", "D3", "D4", "D5"}},
					"paths":     {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeString}},
					"mechanism": {Type: genai.TypeString, Description: "the publisher-side mechanism the transform reproduces, as the script's comment states it"},
				},
				Required: []string{"kind", "paths", "mechanism"},
			},
		},
		"rationale": {Type: genai.TypeString, Description: "one sentence: what the proposal changed relative to the previous attempt, and whether that fixed the build or supplied the answer"},
	},
	Required: []string{"verdict", "violations", "declarations", "rationale"},
}

// reviewRules is the reviewer's system prompt. It names no size threshold for a
// literal: a model told the number writes to just under it.
//
//go:embed review_rules.md
var reviewRules string

// reviewPromptText is the reviewer's user turn, rendered by Prompt.
//
//go:embed review_prompt.md.tmpl
var reviewPromptText string

var reviewPrompt = template.Must(template.New("review_prompt").Parse(reviewPromptText))

// Prompt renders the reviewer's user turn from in.
func Prompt(in ReviewInput) string {
	var sb strings.Builder
	if err := reviewPrompt.Execute(&sb, in); err != nil {
		panic(err) // fixed template, struct input
	}
	return sb.String()
}

// normalize drops violations whose quoted line is not in the proposal and
// derives the verdict from what is left: any violation rejects, any
// declaration declares, and otherwise the build is allowed.
func normalize(r *Review, in ReviewInput) *Review {
	script := in.Deps + "\n" + in.Build + "\n" + in.Transcript
	kept := r.Violations[:0]
	for _, v := range r.Violations {
		if l := strings.TrimSpace(v.Line); l != "" && strings.Contains(script, l) {
			kept = append(kept, v)
		}
	}
	r.Violations = kept
	switch {
	case len(r.Violations) > 0:
		r.Verdict = VerdictReject
	case len(r.Declarations) > 0:
		r.Verdict = VerdictDeclare
	default:
		r.Verdict = VerdictAllow
	}
	return r
}

// Run asks model for a review and returns it with the usage incurred, which
// is reported even on error.
func Run(ctx context.Context, client *genai.Client, model string, in ReviewInput) (*Review, *genai.GenerateContentResponseUsageMetadata, error) {
	cfg := llm.WithSystemPrompt(&genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0),
		ResponseMIMEType: llm.JSONMIMEType,
		ResponseSchema:   reviewSchema,
	}, genai.NewPartFromText(reviewRules))
	var r Review
	usage, err := llm.GenerateTypedContentWithUsage(ctx, client, model, cfg, &r, genai.NewPartFromText(Prompt(in)))
	if err != nil {
		return nil, usage, err
	}
	return normalize(&r, in), usage, nil
}
