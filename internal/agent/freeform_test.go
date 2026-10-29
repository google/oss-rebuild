// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gcsx/gcsxtest"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/genai"
)

var testLocation = rebuild.Location{Repo: "https://github.com/lodash/lodash", Ref: "aa1d7d870d9cf84842ee23ff485fd24abf0ed3d1", Dir: "."}

// scriptedModel answers each generateContent request with the next canned
// response and keeps the request bodies.
type scriptedModel struct {
	responses []string
	requests  [][]byte
}

func (m *scriptedModel) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	m.requests = append(m.requests, body)
	if len(m.requests) > len(m.responses) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"no more responses"}}`))}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(m.responses[len(m.requests)-1]))}, nil
}

// request is the n-th request's JSON body, where a newline is spelled \n.
func (m *scriptedModel) request(n int) string { return string(m.requests[n]) }

func modelCalls(name, args string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"` + name + `","args":` + args + `}}]},"finishReason":"STOP"}]}`
}

func modelSays(text string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":"` + text + `"}]},"finishReason":"STOP"}]}`
}

// A scripted free-form session: a container trial, a matching check, the
// handoff and the model's last word.
const handoffArgs = `{"summary":"the build step was missing","build_commands":{"deps":"npm install -g pnpm@9","build":"npm run build\nnpm pack"}}`

var (
	trial          = modelCalls("run_in_container", `{"command":"npm run build && npm pack"}`)
	check          = modelCalls("check_artifact", `{"path":"/src/lodash-4.17.21.tgz"}`)
	handoffCall    = modelCalls("handoff", handoffArgs)
	done           = modelSays("done")
	freeFormScript = []string{trial, check, handoffCall, done}
)

// checkResult is one check_artifact exec's result: hashes and stabilized forms
// on the first line, then diffr's summary on a mismatch.
type checkResult struct {
	code int
	out  string
}

const stabilizedUpstream = "/home/builder/upstream/lodash-4.17.21.tgz.stabilized"

var (
	vmMatch    = checkResult{code: 0, out: "ab ab /tmp/check-2/candidate.stabilized " + stabilizedUpstream + "\n"}
	vmMismatch = checkResult{code: 1, out: "ab cd /tmp/check-1/candidate.stabilized " + stabilizedUpstream + "\nwithin candidate (vs upstream):\n  differ (1):\n    package/index.js\n"}
)

// freeFormHarness is a free-form agent on a scripted model and VM. Check execs
// answer checks in order, then match, and other execs exit 0.
type freeFormHarness struct {
	a       *defaultAgent
	model   *scriptedModel
	mu      sync.Mutex
	execs   []schema.ScratchExecRequest
	checks  []checkResult
	records map[string][]byte
}

func freeFormTestAgent(t *testing.T, responses []string) *freeFormHarness {
	t.Helper()
	ctx := context.Background()
	h := &freeFormHarness{model: &scriptedModel{responses: responses}, records: map[string][]byte{}}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: "test", Backend: genai.BackendGeminiAPI, HTTPClient: &http.Client{Transport: h.model}})
	if err != nil {
		t.Fatal(err)
	}
	runner := testRunner(&fakeExecutor{result: build.Result{Error: &scratch.ExitError{Code: 1}}, logs: []byte("npm ERR! missing script: build\n")})
	runner.RegistryClient = &fakeRegistry{}
	runner.Run(ctx, "iter-1", testStrategy())
	outputs := gcsxtest.NewServer(t, nil)
	runner.GCSClient = outputs.Client
	runner.Stubs.ExecCreate = func(_ context.Context, req schema.ScratchExecRequest) (*longrunning.Operation[schema.ScratchExecResult], error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.execs = append(h.execs, req)
		id, code := fmt.Sprintf("op-%d", len(h.execs)), 0
		if strings.Contains(req.Cmd[2], "--label candidate") {
			r := vmMatch
			if len(h.checks) > 0 {
				r, h.checks = h.checks[0], h.checks[1:]
			}
			code = r.code
			outputs.Put("out/"+id, r.out)
		}
		return &longrunning.Operation[schema.ScratchExecResult]{ID: id, Done: true, Result: &schema.ScratchExecResult{ScratchID: "s1", ExitCode: code, OutURI: "gs://out/" + id}}, nil
	}
	h.a = NewDefaultAgent(testTarget, &AgentDeps{ScratchRunner: runner, MaxTurns: 7})
	h.a.loc = testLocation
	h.a.enableFreeForm(runner, func(ctx context.Context, system string, tools []*llm.FunctionDefinition) (*llm.Chat, error) {
		return llm.NewChat(ctx, client, "m", llm.WithSystemPrompt(nil, genai.NewPartFromText(system)), &llm.ChatOpts{Tools: tools, MaxToolIterations: 20})
	})
	h.a.ff.store = func(_ context.Context, _ *ProposeOpts, name string, v any) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.records[name], _ = json.Marshal(v)
	}
	h.a.RecordIteration(&schema.AgentIteration{Number: 1, Strategy: testStrategy(), Status: schema.AgentIterationStatusFailed, Result: &schema.AgentBuildResult{ErrorMessage: "build failed in build phase"}})
	return h
}

func (h *freeFormHarness) propose(t *testing.T) *schema.StrategyOneOf {
	t.Helper()
	dir, _ := url.Parse("gs://sessions/s1/messages/2")
	s, err := h.a.Propose(context.Background(), &ProposeOpts{ChatUploadURL: dir})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return s
}

func (h *freeFormHarness) fail(msg string) {
	h.a.RecordIteration(&schema.AgentIteration{Number: len(h.a.iterHistory) + 1, Status: schema.AgentIterationStatusFailed, Result: &schema.AgentBuildResult{ErrorMessage: msg}})
}

// record returns the transcript object whose name ends in suffix.
func (h *freeFormHarness) record(t *testing.T, suffix string) map[string]json.RawMessage {
	t.Helper()
	for name, b := range h.records {
		if strings.HasSuffix(name, suffix) {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return m
		}
	}
	t.Fatalf("no record ends in %s: %v", suffix, h.records)
	return nil
}

// wantAsWritten is the scripted handoff's commands on the attempt's deps.
func wantAsWritten(loc rebuild.Location) *rebuild.ManualStrategy {
	return &rebuild.ManualStrategy{
		Location:   loc,
		Requires:   rebuild.RequiredEnv{SystemDeps: []string{"git"}},
		Deps:       "npm install\nnpm install -g pnpm@9",
		Build:      "npm run build\nnpm pack",
		OutputPath: "lodash-4.17.21.tgz",
	}
}

func TestFreeFormProposesTheHandoff(t *testing.T) {
	h := freeFormTestAgent(t, freeFormScript)
	got, _ := h.propose(t).Strategy()
	if diff := cmp.Diff(wantAsWritten(testLocation), got); diff != "" {
		t.Errorf("strategy mismatch (-want +got):\n%s", diff)
	}
	if len(h.execs) != 3 {
		t.Fatalf("execs = %d, want the prepare, the trial and the check", len(h.execs))
	}
	prepare := h.execs[0].Cmd[2]
	for _, want := range []string{"fetch /home/builder/upstream/lodash-4.17.21.tgz https://registry.example/lodash-4.17.21.tgz", "stab /home/builder/upstream/lodash-4.17.21.tgz " + stabilizedUpstream + "\n"} {
		if !strings.Contains(prepare, want) {
			t.Errorf("prepare script lacks %q:\n%s", want, prepare)
		}
	}
	if strings.Contains(prepare, "diffr --summary") {
		t.Errorf("prepare script compares:\n%s", prepare)
	}
	if n := len(h.model.requests); n != 4 {
		t.Fatalf("%d model calls, want 4: three tool calls and the answer", n)
	}
	first := h.model.request(0)
	for _, want := range []string{"call handoff once", `"name":"check_artifact"`, `"name":"handoff"`, `"name":"run_in_container"`, "Work in the container until check_artifact reports a match, then call handoff"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first model request lacks %q", want)
		}
	}
	// The handoff is on file beside the transcript.
	hr := h.record(t, "-handoff.json")
	var trace []toolCall
	if err := json.Unmarshal(hr["trace"], &trace); err != nil || len(trace) != 3 || trace[2].Tool != "handoff" || trace[0].Output != "[exit 0]" || !strings.HasPrefix(trace[1].Output, "MATCH") {
		t.Errorf("handoff record trace = %s (%v)", hr["trace"], err)
	}
	if !bytes.Contains(hr["handoff"], []byte(`"npm install -g pnpm@9"`)) {
		t.Errorf("handoff record = %s", hr["handoff"])
	}
	var steps int
	for name := range h.records {
		if strings.HasSuffix(name, "-user.json") || strings.HasSuffix(name, "-model.json") {
			steps++
		}
	}
	if steps < 7 {
		t.Errorf("%d chat contents recorded, want the turn's prompts and answers", steps)
	}
	if len(h.a.thoughts) != 1 || h.a.thoughts[0].Diagnostic != "the build step was missing" {
		t.Errorf("thoughts = %+v", h.a.thoughts)
	}
}

func TestFreeFormStartsOverAfterTheHandoff(t *testing.T) {
	h := freeFormTestAgent(t, append(append([]string{}, freeFormScript...), freeFormScript...))
	h.propose(t)
	h.fail("build failed with exit code 1")
	// The handoff is spent, so a fresh session runs to a fresh handoff.
	got, _ := h.propose(t).Strategy()
	if ms, ok := got.(*rebuild.ManualStrategy); !ok || ms.Build != "npm run build\nnpm pack" {
		t.Errorf("strategy = %+v, want the second handoff's commands", got)
	}
	if n := len(h.model.requests); n != 8 {
		t.Fatalf("%d model calls, want a second session (8)", n)
	}
	if second := h.model.request(4); !strings.Contains(second, "### Thought 1") || !strings.Contains(second, "the build step was missing") {
		t.Errorf("the second session's prompt lacks the first handoff as a thought:\n%s", second)
	}
}

func TestFreeFormHandoffWithoutBuildCommands(t *testing.T) {
	empty := modelCalls("handoff", `{"summary":"s","build_commands":{"deps":"","build":" "}}`)
	h := freeFormTestAgent(t, []string{check, empty, done})
	if _, err := h.a.Propose(context.Background(), &ProposeOpts{}); err == nil || !strings.Contains(err.Error(), "handoff without build commands") {
		t.Errorf("err = %v, want the empty handoff rejected", err)
	}
	if h.a.ff.pending != nil {
		t.Error("an empty handoff was kept")
	}
}

func TestFreeFormRemindsTwiceThenFails(t *testing.T) {
	h := freeFormTestAgent(t, []string{modelSays("looking"), modelSays("still looking"), modelSays("giving up")})
	if _, err := h.a.Propose(context.Background(), &ProposeOpts{}); err == nil || !strings.Contains(err.Error(), "the model ended without calling handoff") {
		t.Errorf("err = %v, want no handoff", err)
	}
	if len(h.model.requests) != 3 || !strings.Contains(h.model.request(1), handoffNoMatch) || !strings.Contains(h.model.request(2), handoffNoMatch) {
		t.Errorf("%d model calls, want three turns, the last two the no-match reminder", len(h.model.requests))
	}
	if h.a.ff.pending != nil {
		t.Error("a session without a handoff was kept")
	}
}

// Each reminder fits the trace: keep iterating before a match, hand off after.
func TestFreeFormRemindsThenHandsOff(t *testing.T) {
	h := freeFormTestAgent(t, []string{trial, check, modelSays("hm"), trial, check, modelSays("built it"), handoffCall, done})
	h.checks = []checkResult{vmMismatch, vmMatch}
	got, _ := h.propose(t).Strategy()
	if diff := cmp.Diff(wantAsWritten(testLocation), got); diff != "" {
		t.Errorf("strategy mismatch (-want +got):\n%s", diff)
	}
	if n := len(h.model.requests); n != 8 {
		t.Fatalf("%d model calls, want 8 across three turns", n)
	}
	if !strings.Contains(h.model.request(3), handoffNoMatch) || !strings.Contains(h.model.request(6), handoffMatched) {
		t.Errorf("follow-ups: want the no-match reminder on the second turn and the matched one on the third")
	}
}

func TestFreeFormEarlyHandoffIsReminded(t *testing.T) {
	early := modelCalls("handoff", `{"summary":"guess","build_commands":{"deps":"","build":"npm pack"}}`)
	h := freeFormTestAgent(t, []string{early, modelSays("I handed off."), check, handoffCall, done})
	h.propose(t)
	if n := len(h.model.requests); n != 5 {
		t.Fatalf("%d model calls, want 5 across the two turns", n)
	}
	// The early answer reached the model, then the reminder in the same chat.
	if second := h.model.request(1); !strings.Contains(second, "has not reported a match") {
		t.Errorf("the early handoff's answer is not in the chat:\n%s", second)
	}
	if third := h.model.request(2); !strings.Contains(third, "guess") || !strings.Contains(third, handoffEarly) {
		t.Errorf("the reminder's turn lost the chat's history or the reminder:\n%s", third)
	}
}

func TestHandoffReminder(t *testing.T) {
	match := toolCall{Tool: "check_artifact", Output: "MATCH: the stabilized candidate equals the stabilized upstream (sha256 ab)."}
	mismatch := toolCall{Tool: "check_artifact", Output: "MISMATCH: stabilized sha256 differ"}
	judged := toolCall{Tool: "handoff", Output: "recorded"}
	early := toolCall{Tool: "handoff", Output: "check_artifact has not reported a match in this session", IsError: true}
	for name, tc := range map[string]struct {
		trace []toolCall
		want  string
	}{
		"no calls":                          {nil, handoffNoMatch},
		"mismatch":                          {[]toolCall{mismatch}, handoffNoMatch},
		"match":                             {[]toolCall{mismatch, match}, handoffMatched},
		"a later mismatch keeps the match":  {[]toolCall{match, mismatch}, handoffMatched},
		"a judged handoff spends it":        {[]toolCall{match, judged, mismatch}, handoffNoMatch},
		"a handoff before a match does not": {[]toolCall{early, match}, handoffMatched},
		"a match after a judged handoff":    {[]toolCall{match, judged, match}, handoffMatched},
	} {
		if got := handoffReminder(tc.trace); got != tc.want {
			t.Errorf("%s: handoffReminder = %.40q, want %.40q", name, got, tc.want)
		}
	}
}
