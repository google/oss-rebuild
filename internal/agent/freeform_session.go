// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
	"google.golang.org/genai"
)

// A free-form session is a job-side chat with check_artifact, handoff and
// give_up beside its tools. None of the loop runs on the VM.

// openFreeForm is the driver's sessionOpener over a fresh chat.
func (a *defaultAgent) openFreeForm(ctx context.Context, opts *ProposeOpts, prev *executionDetails) (freeFormSession, string, error) {
	s := &chatSession{a: a, opts: opts, check: artifactCheck{runner: a.ff.runner}}
	tools := append(a.getTools(), s.check.tool(), s.handoffTool(), giveUpTool(func(g giveUpArgs) { s.giveUp = &g }))
	chat, err := a.newChat(ctx, freeFormPrompt, s.tracing(tools))
	if err != nil {
		return nil, "", errors.Wrap(err, "starting the free-form chat")
	}
	s.chat = chat
	prompt := strings.Join(append(a.historyContext(prev), jobFreeFormGuidance()...), "\n")
	return s, prompt, nil
}

type chatSession struct {
	a       *defaultAgent
	opts    *ProposeOpts
	chat    *llm.Chat
	check   artifactCheck
	trace   []toolCall      // every tool call, as executed
	handoff json.RawMessage // the turn's handoff, if recorded
	giveUp  *giveUpArgs     // the turn's give_up, if recorded
	part    int             // transcript contents written
}

func (s *chatSession) run(ctx context.Context, prompt string) (json.RawMessage, []toolCall, error) {
	return s.turn(ctx, prompt)
}

func (s *chatSession) followUp(ctx context.Context, prompt string) (json.RawMessage, []toolCall, error) {
	return s.turn(ctx, prompt)
}

// turn sends prompt and runs the tool loop to the model's answer, filing each
// content as it arrives. A give_up ends the turn in a gaveUp error, even after
// a handoff.
func (s *chatSession) turn(ctx context.Context, prompt string) (json.RawMessage, []toolCall, error) {
	s.handoff = nil
	parent := ctx
	if w := s.a.ff.windDown; !w.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, w.Add(windDownGrace))
		defer cancel()
	}
	var err error
	for content, cerr := range s.chat.SendMessageStream(ctx, genai.NewPartFromText(prompt)) {
		if cerr != nil {
			err = cerr
			break
		}
		s.part++
		s.a.ff.record(ctx, s.opts, fmt.Sprintf("%s-%d-%s.json", stamp(), s.part, content.Role), content)
	}
	if g := s.giveUp; g != nil {
		s.giveUp = nil
		s.a.ff.record(ctx, s.opts, stamp()+"-giveup.json", giveUpRecord{giveUpArgs: *g, JobClosest: s.check.closest})
		return nil, s.trace, gaveUp{Reason: g.Reason, Closest: s.check.closest}
	}
	// The wind-down ended the turn: the job gives up for the model.
	if ctx.Err() != nil && parent.Err() == nil {
		s.a.ff.record(parent, s.opts, stamp()+"-giveup.json", giveUpRecord{giveUpArgs: giveUpArgs{Reason: timeRanOut}, JobClosest: s.check.closest})
		return nil, s.trace, gaveUp{Reason: timeRanOut, Closest: s.check.closest}
	}
	if err != nil {
		return nil, s.trace, errors.Wrap(err, "running the free-form chat")
	}
	return s.handoff, s.trace, nil
}

// giveUpRecord is the <stamp>-giveup.json transcript object: the model's
// give_up beside the closest candidate the job compared.
type giveUpRecord struct {
	giveUpArgs
	JobClosest *schema.AgentCandidate `json:"job_closest,omitempty"`
}

// close folds the chat's tokens into the agent's total.
func (s *chatSession) close() {
	s.a.sideUsage = s.a.sideUsage.Add(sumTokenUsage(s.chat.Usage(), s.chat.Model()))
}

// handoffTool records the turn's handoff. Before a match it records nothing,
// so the driver's reminder continues the session.
func (s *chatSession) handoffTool() *llm.FunctionDefinition {
	return &llm.FunctionDefinition{
		FunctionDeclaration: genai.FunctionDeclaration{
			Name:        "handoff",
			Description: "Hand off the commands that produced the matching artifact, once check_artifact has reported a match. The job formalizes them into a build definition and verifies it in a fresh container.",
			Parameters: &genai.Schema{
				Type:     genai.TypeObject,
				Required: []string{"summary", "build_commands"},
				Properties: map[string]*genai.Schema{
					"summary": {Type: genai.TypeString, Description: "Two or three sentences: what kept the build from matching and what fixed it"},
					"build_commands": {
						Type:     genai.TypeObject,
						Required: []string{"deps", "build"},
						Properties: map[string]*genai.Schema{
							"deps":  {Type: genai.TypeString, Description: "Every command that installs or fetches anything, run in /src after the previous attempt's deps phase"},
							"build": {Type: genai.TypeString, Description: "The rest: what produced the artifact"},
						},
					},
				},
			},
			Response: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"result": {Type: genai.TypeString}, "error": {Type: genai.TypeString}}},
		},
		Function: func(_ context.Context, args map[string]any) genai.FunctionResponse {
			if !s.check.matched {
				return genai.FunctionResponse{Name: "handoff", Response: map[string]any{"error": earlyHandoffResult}}
			}
			raw, _ := json.Marshal(args)
			s.handoff, s.check.matched = raw, false
			return genai.FunctionResponse{Name: "handoff", Response: map[string]any{"result": "Recorded. End your turn now."}}
		},
	}
}

// tracing wraps tools to append each call to the trace and, past windDown,
// add windDownNotice to its response.
func (s *chatSession) tracing(defs []*llm.FunctionDefinition) []*llm.FunctionDefinition {
	out := make([]*llm.FunctionDefinition, 0, len(defs))
	for _, def := range defs {
		name, fn := def.Name, def.Function
		wrapped := *def
		wrapped.Function = func(ctx context.Context, args map[string]any) genai.FunctionResponse {
			resp := fn(ctx, args)
			if w := s.a.ff.windDown; !w.IsZero() && time.Now().After(w) {
				if resp.Response == nil {
					resp.Response = map[string]any{}
				}
				resp.Response["notice"] = windDownNotice
			}
			argsJSON, _ := json.Marshal(args)
			respJSON, _ := json.Marshal(resp.Response)
			s.trace = append(s.trace, traced(name, string(argsJSON), string(respJSON), nil))
			return resp
		}
		out = append(out, &wrapped)
	}
	return out
}

// traced is one tool execution as the trace keeps it: output then exit code
// for a shell, the verdict for check_artifact, the response otherwise.
func traced(name, args, resp string, err error) toolCall {
	c := toolCall{Tool: name, Args: json.RawMessage(args)}
	if err != nil {
		c.Output, c.IsError = err.Error(), true
		return c
	}
	var r struct {
		Output  string `json:"output"`
		Exit    int    `json:"exit_code"`
		Error   string `json:"error"`
		Omitted int    `json:"omitted_bytes"`
		Result  string `json:"result"`
	}
	_ = json.Unmarshal([]byte(resp), &r)
	c.IsError = r.Error != ""
	switch name {
	case "run_on_host", "run_in_container":
		var b strings.Builder
		b.WriteString(r.Output)
		if r.Output != "" && !strings.HasSuffix(r.Output, "\n") {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[exit %d", r.Exit)
		if r.Omitted > 0 {
			fmt.Fprintf(&b, ", %d bytes omitted from the middle", r.Omitted)
		}
		b.WriteByte(']')
		if r.Error != "" {
			b.WriteString("\n" + r.Error)
		}
		c.Output = b.String()
	case "check_artifact":
		c.Output = cmp.Or(r.Result, r.Error)
	default:
		c.Output = resp
	}
	return c
}
