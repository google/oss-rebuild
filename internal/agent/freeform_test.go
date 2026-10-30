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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/buildpolicy"
	"github.com/google/oss-rebuild/internal/gcsx/gcsxtest"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/scratch"
	"github.com/google/oss-rebuild/pkg/llm"
	"github.com/google/oss-rebuild/pkg/longrunning"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
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

const formalized = `{"deps": [{"runs": "npm install"}, {"runs": "npm install -g pnpm@9"}], "build": [{"runs": "npm run build\nnpm pack"}], "requires": {"system_deps": ["git"]}, "output_path": "lodash-4.17.21.tgz"}`

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
// answer checks in order, then match, and other execs exit 0. Formalizations
// answer in order, then fail.
type freeFormHarness struct {
	a       *defaultAgent
	model   *scriptedModel
	mu      sync.Mutex
	execs   []schema.ScratchExecRequest
	checks  []checkResult
	prompts []string
	records map[string][]byte
}

func freeFormTestAgent(t *testing.T, responses []string, answers ...string) *freeFormHarness {
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
	h.a = NewDefaultAgent(testTarget, &AgentDeps{Shell: runner, MaxTurns: 7})
	h.a.loc = testLocation
	h.a.enableFreeForm(runner, func(ctx context.Context, system string, tools []*llm.FunctionDefinition) (*llm.Chat, error) {
		return llm.NewChat(ctx, client, "m", llm.WithSystemPrompt(nil, genai.NewPartFromText(system)), &llm.ChatOpts{Tools: tools, MaxToolIterations: 20})
	})
	h.a.ff.complete = func(_ context.Context, prompt string) (string, error) {
		h.prompts = append(h.prompts, prompt)
		if len(h.prompts) > len(answers) {
			return "", errors.New("the model is unavailable")
		}
		return answers[len(h.prompts)-1], nil
	}
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

func wantWorkflow(loc rebuild.Location) *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Location:   loc,
		Source:     []flow.Step{{Uses: "git-checkout"}},
		Deps:       []flow.Step{{Runs: "npm install"}, {Runs: "npm install -g pnpm@9"}},
		Build:      []flow.Step{{Runs: "npm run build\nnpm pack"}},
		Requires:   rebuild.RequiredEnv{SystemDeps: []string{"git"}},
		OutputPath: "lodash-4.17.21.tgz",
	}
}

func TestFreeFormProposeFormalizesTheHandoff(t *testing.T) {
	h := freeFormTestAgent(t, freeFormScript, formalized)
	got, _ := h.propose(t).Strategy()
	if diff := cmp.Diff(wantWorkflow(testLocation), got); diff != "" {
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
	for _, want := range []string{"call handoff once", `"name":"check_artifact"`, `"name":"handoff"`, `"name":"give_up"`, `"name":"run_in_container"`, "Work in the container until check_artifact reports a match, then call handoff"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first model request lacks %q", want)
		}
	}
	// The formalization sees the exemplar, the handoff, and the trace.
	if len(h.prompts) != 1 {
		t.Fatalf("formalizations = %d, want 1", len(h.prompts))
	}
	for _, s := range []string{
		"npm package lodash version 4.17.21 (artifact lodash-4.17.21.tgz)",
		"Its deps phase:\n```sh\nnpm install\n```",
		"system_deps: git",
		"The model's summary: the build step was missing",
		"```sh\nnpm install -g pnpm@9\n```",
		"## 1. run_in_container $ npm run build && npm pack\n```\n[exit 0]\n```",
		`## 2. check_artifact {"path":"/src/lodash-4.17.21.tgz"}`,
		"## 3. handoff {",
		formalizeShape,
	} {
		if !strings.Contains(h.prompts[0], s) {
			t.Errorf("formalization prompt lacks %q:\n%s", s, h.prompts[0])
		}
	}
	if strings.Contains(h.prompts[0], "Rejected definitions") {
		t.Error("the first formalization lists rejections")
	}
	// The handoff and the formalization are on file beside the transcript.
	hr := h.record(t, "-handoff.json")
	var trace []toolCall
	if err := json.Unmarshal(hr["trace"], &trace); err != nil || len(trace) != 3 || trace[2].Tool != "handoff" || trace[0].Output != "[exit 0]" || !strings.HasPrefix(trace[1].Output, "MATCH") {
		t.Errorf("handoff record trace = %s (%v)", hr["trace"], err)
	}
	if !bytes.Contains(hr["handoff"], []byte(`"npm install -g pnpm@9"`)) {
		t.Errorf("handoff record = %s", hr["handoff"])
	}
	fr := h.record(t, "-formalize-1.json")
	if string(fr["attempt"]) != "1" || !bytes.Contains(fr["strategy"], []byte(`"flow"`)) || fr["error"] != nil {
		t.Errorf("formalize record = %v", fr)
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

func TestFreeFormRetriesThenFallsBackThenStartsOver(t *testing.T) {
	h := freeFormTestAgent(t, append(append([]string{}, freeFormScript...), freeFormScript...), formalized, formalized, formalized, formalized)
	h.propose(t)
	h.fail("rebuild content mismatch\n\nwithin rebuild (vs upstream):\n  differ (1):\n    package/index.js")
	// Two re-formalizations of the same handoff, each told the rejections so far.
	for n := 2; n <= 3; n++ {
		got, _ := h.propose(t).Strategy()
		if _, ok := got.(*rebuild.WorkflowStrategy); !ok {
			t.Fatalf("proposal %d = %T, want a WorkflowStrategy", n, got)
		}
		if len(h.prompts) != n || len(h.model.requests) != 4 {
			t.Fatalf("after proposal %d: %d formalizations and %d model calls, want %d and 4", n, len(h.prompts), len(h.model.requests), n)
		}
		for _, s := range []string{"# Rejected definitions", "## Candidate 1\n```\nrebuild content mismatch", "package/index.js"} {
			if !strings.Contains(h.prompts[n-1], s) {
				t.Errorf("formalization %d lacks %q:\n%s", n, s, h.prompts[n-1])
			}
		}
		h.fail("rebuild content mismatch\n\nwithin rebuild (vs upstream):\n  differ (1):\n    package/index.js")
	}
	if !strings.Contains(h.prompts[2], "## Candidate 2") {
		t.Errorf("the third formalization lacks the second rejection:\n%s", h.prompts[2])
	}
	if _, ok := h.records["-formalize-3.json"]; ok {
		t.Error("record names carry no stamp")
	}
	// Then the handoff's commands as written, on the exemplar's deps.
	got, _ := h.propose(t).Strategy()
	want := &rebuild.ManualStrategy{
		Location:   testLocation,
		Requires:   rebuild.RequiredEnv{SystemDeps: []string{"git"}},
		Deps:       "npm install\nnpm install -g pnpm@9",
		Build:      "npm run build\nnpm pack",
		OutputPath: "lodash-4.17.21.tgz",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fallback mismatch (-want +got):\n%s", diff)
	}
	if len(h.prompts) != 3 || len(h.model.requests) != 4 {
		t.Fatalf("the fallback cost %d formalizations and %d model calls, want 3 and 4", len(h.prompts), len(h.model.requests))
	}
	h.fail("build failed with exit code 1")
	// Then a fresh session and a fresh handoff.
	if _, err := h.propose(t).Strategy(); err != nil {
		t.Fatalf("no strategy from the fresh session: %v", err)
	}
	if len(h.model.requests) != 8 || len(h.prompts) != 4 {
		t.Errorf("after the fallback: %d model calls and %d formalizations, want a new session (8) and a new formalization (4)", len(h.model.requests), len(h.prompts))
	}
	if strings.Contains(h.prompts[3], "Rejected definitions") || h.a.ff.pending.attempts != 1 {
		t.Errorf("the fresh handoff carries the old rejections: attempts=%d\n%s", h.a.ff.pending.attempts, h.prompts[3])
	}
	if second := h.model.request(4); !strings.Contains(second, "### Thought 1") || !strings.Contains(second, "the build step was missing") {
		t.Errorf("the second session's prompt lacks the first handoff as a thought:\n%s", second)
	}
}

func TestFreeFormRejectedFormalizationIsRetoldInThePropose(t *testing.T) {
	h := freeFormTestAgent(t, freeFormScript, "not json", formalized)
	got, _ := h.propose(t).Strategy()
	if diff := cmp.Diff(wantWorkflow(testLocation), got); diff != "" {
		t.Errorf("strategy mismatch (-want +got):\n%s", diff)
	}
	// No build ran, so the retry is in the same Propose and session.
	if len(h.model.requests) != 4 || len(h.prompts) != 2 || !strings.Contains(h.prompts[1], "## Candidate 1\n```\nrejected before any build: decoding the strategy") {
		t.Errorf("model calls = %d, formalizations = %d, prompt:\n%s", len(h.model.requests), len(h.prompts), h.prompts[len(h.prompts)-1])
	}
	if fr := h.record(t, "-formalize-1.json"); !bytes.Contains(fr["error"], []byte("rejected before any build: decoding the strategy")) || !bytes.Contains(fr["response"], []byte("not json")) || fr["strategy"] != nil {
		t.Errorf("formalize-1 record = %v, want the response and the error", fr)
	}
	if fr := h.record(t, "-formalize-2.json"); fr["error"] != nil || !bytes.Contains(fr["strategy"], []byte(`"flow"`)) {
		t.Errorf("formalize-2 record = %v, want the strategy", fr)
	}
	if h.a.ff.pending.attempts != 2 || len(h.a.ff.pending.failures) != 1 {
		t.Errorf("attempts = %d, failures = %q; want 2 and 1", h.a.ff.pending.attempts, h.a.ff.pending.failures)
	}
}

func TestFreeFormModelFailureIsNotAnAttempt(t *testing.T) {
	h := freeFormTestAgent(t, freeFormScript)
	if _, err := h.a.Propose(context.Background(), &ProposeOpts{}); err == nil || !strings.Contains(err.Error(), "the model is unavailable") {
		t.Fatalf("err = %v, want the model failure", err)
	}
	if h.a.ff.pending == nil || h.a.ff.pending.attempts != 0 || len(h.a.ff.pending.failures) != 0 {
		t.Errorf("ff = %+v, want the handoff kept with no attempt", h.a.ff.pending)
	}
}

func TestFreeFormHandoffWithoutBuildCommands(t *testing.T) {
	empty := modelCalls("handoff", `{"summary":"s","build_commands":{"deps":"","build":" "}}`)
	h := freeFormTestAgent(t, []string{check, empty, done}, formalized)
	if _, err := h.a.Propose(context.Background(), &ProposeOpts{}); err == nil || !strings.Contains(err.Error(), "handoff without build commands") {
		t.Errorf("err = %v, want the empty handoff rejected", err)
	}
	if h.a.ff.pending != nil || len(h.prompts) != 0 {
		t.Error("an empty handoff was kept or formalized")
	}
}

func TestFreeFormRemindsTwiceThenFails(t *testing.T) {
	h := freeFormTestAgent(t, []string{modelSays("looking"), modelSays("still looking"), modelSays("giving up")}, formalized)
	if _, err := h.a.Propose(context.Background(), &ProposeOpts{}); err == nil || !strings.Contains(err.Error(), "the model ended without calling handoff") {
		t.Errorf("err = %v, want no handoff", err)
	}
	if len(h.model.requests) != 3 || !strings.Contains(h.model.request(1), handoffNoMatch) || !strings.Contains(h.model.request(2), handoffNoMatch) {
		t.Errorf("%d model calls, want three turns, the last two the no-match reminder", len(h.model.requests))
	}
	if len(h.prompts) != 0 || h.a.ff.pending != nil {
		t.Error("a session without a handoff was formalized")
	}
}

// Each reminder fits the trace: keep iterating before a match, hand off after.
func TestFreeFormRemindsThenHandsOff(t *testing.T) {
	h := freeFormTestAgent(t, []string{trial, check, modelSays("hm"), trial, check, modelSays("built it"), handoffCall, done}, formalized)
	h.checks = []checkResult{vmMismatch, vmMatch}
	got, _ := h.propose(t).Strategy()
	if diff := cmp.Diff(wantWorkflow(testLocation), got); diff != "" {
		t.Errorf("strategy mismatch (-want +got):\n%s", diff)
	}
	if n := len(h.model.requests); n != 8 {
		t.Fatalf("%d model calls, want 8 across three turns", n)
	}
	if !strings.Contains(h.model.request(3), handoffNoMatch) || !strings.Contains(h.model.request(6), handoffMatched) {
		t.Errorf("follow-ups: want the no-match reminder on the second turn and the matched one on the third")
	}
	// The handoff is formalized from the whole session's commands.
	if len(h.prompts) != 1 || !strings.Contains(h.prompts[0], "## 5. handoff {") || !strings.Contains(h.prompts[0], "MISMATCH: stabilized sha256 differ") {
		t.Errorf("formalizations = %d, prompt:\n%.1200s", len(h.prompts), strings.Join(h.prompts, "\n"))
	}
}

func TestFreeFormEarlyHandoffIsReminded(t *testing.T) {
	early := modelCalls("handoff", `{"summary":"guess","build_commands":{"deps":"","build":"npm pack"}}`)
	h := freeFormTestAgent(t, []string{early, modelSays("I handed off."), check, handoffCall, done}, formalized)
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

func TestDecodeWorkflow(t *testing.T) {
	loc := rebuild.Location{Repo: "https://github.com/lodash/lodash", Ref: "aa1d", Dir: "."}
	exemplar := rebuild.Instructions{OutputPath: "lodash-4.17.21.tgz", Requires: rebuild.RequiredEnv{SystemDeps: []string{"git", "make"}, BaseImage: "img"}}
	be := rebuild.BuildEnv{TimewarpHost: "localhost:8080"}
	for _, tt := range []struct {
		name    string
		out     string
		want    *rebuild.WorkflowStrategy
		wantErr string
	}{
		{
			name: "FillsFromExemplar",
			out:  "```json\n{\"deps\": [{\"runs\": \"npm install\", \"needs\": [\"curl\"]}], \"build\": [{\"runs\": \"npm pack\"}]}\n```",
			want: &rebuild.WorkflowStrategy{
				Location:   loc,
				Source:     []flow.Step{{Uses: "git-checkout"}},
				Deps:       []flow.Step{{Runs: "npm install", Needs: []string{"curl"}}},
				Build:      []flow.Step{{Runs: "npm pack"}},
				Requires:   rebuild.RequiredEnv{SystemDeps: []string{"git", "make"}, BaseImage: "img"},
				OutputPath: "lodash-4.17.21.tgz",
			},
		},
		{
			name: "KeepsModelFields",
			out:  `{"src": [{"runs": "git checkout x"}], "deps": [], "build": [{"runs": "npm pack"}], "requires": {"system_deps": []}, "output_path": "dist/x.tgz", "output_dir": "dist"}`,
			want: &rebuild.WorkflowStrategy{
				Location:   loc,
				Source:     []flow.Step{{Runs: "git checkout x"}},
				Deps:       []flow.Step{},
				Build:      []flow.Step{{Runs: "npm pack"}},
				Requires:   rebuild.RequiredEnv{SystemDeps: []string{}, BaseImage: "img"},
				OutputPath: "dist/x.tgz",
			},
		},
		{name: "NoBuildSteps", out: `{"deps": [{"runs": "npm install"}]}`, wantErr: "no build steps"},
		{name: "NotJSON", out: "npm pack", wantErr: "decoding the strategy"},
		{name: "TemplateSyntaxInScript", out: `{"build": [{"runs": "echo {{ .Nope"}]}`, wantErr: "rendering the strategy"},
		{name: "RunsAndUsesInOneStep", out: `{"build": [{"runs": "npm pack", "uses": "git-checkout"}]}`, wantErr: "rendering the strategy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeWorkflow(tt.out, loc, exemplar, testTarget, be)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("strategy mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFormalizePromptTruncatesTraceOutput(t *testing.T) {
	ff := &freeForm{trace: []toolCall{{Tool: "run_on_host", Args: json.RawMessage(`{"command":"cat big"}`), Output: strings.Repeat("x", traceHead+traceTail+10), IsError: true}}}
	p := formalizePrompt(testTarget, ff)
	for _, s := range []string{"## 1. run_on_host $ cat big\n(error result)\n```\n" + strings.Repeat("x", traceHead) + "\n...[10 bytes omitted"} {
		if !strings.Contains(p, s) {
			t.Errorf("prompt lacks %q:\n%.600s", s, p)
		}
	}
}

// candidateReviews returns the review records of candidates, not handoffs.
func (h *freeFormHarness) candidateReviews(t *testing.T) map[string]map[string]json.RawMessage {
	t.Helper()
	out := map[string]map[string]json.RawMessage{}
	for name, b := range h.records {
		if !strings.HasSuffix(name, "-review.json") || strings.HasSuffix(name, "-handoff-review.json") {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = m
	}
	return out
}

// allowing is a reviewer that allows every proposal and keeps what it saw.
func allowing(seen *[]buildpolicy.ReviewInput) func(context.Context, buildpolicy.ReviewInput) (*buildpolicy.Review, *genai.GenerateContentResponseUsageMetadata, error) {
	return func(_ context.Context, in buildpolicy.ReviewInput) (*buildpolicy.Review, *genai.GenerateContentResponseUsageMetadata, error) {
		*seen = append(*seen, in)
		return &buildpolicy.Review{Verdict: buildpolicy.VerdictAllow, Rationale: "toolchain configuration"}, nil, nil
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// transcribed writes the shipped file from a literal. The handoff's commands
// stay clean, so only the session's transcript shows it.
var transcribed = "cat > /src/index.js <<'EOF'\n" + strings.Repeat("module.exports = {};\n", 30) + "EOF\nnpm pack"

var transcribedCall = modelCalls("run_in_container", `{"command":`+jsonString(transcribed)+`}`)

// rejecting is a reviewer that rejects under rule any input containing a mark,
// and allows the rest.
func rejecting(rule string, marks ...string) func(context.Context, buildpolicy.ReviewInput) (*buildpolicy.Review, *genai.GenerateContentResponseUsageMetadata, error) {
	return func(_ context.Context, in buildpolicy.ReviewInput) (*buildpolicy.Review, *genai.GenerateContentResponseUsageMetadata, error) {
		for _, text := range []string{in.Deps, in.Build, in.Transcript} {
			for _, mark := range marks {
				if i := strings.Index(text, mark); i >= 0 {
					line := text[i:]
					if j := strings.IndexByte(line, '\n'); j >= 0 {
						line = line[:j]
					}
					return &buildpolicy.Review{Verdict: buildpolicy.VerdictReject, Violations: []buildpolicy.Violation{{Rule: rule, Line: line, Why: "supplies the content instead of building it"}}, Rationale: "the match was written, not built"}, nil, nil
				}
			}
		}
		return &buildpolicy.Review{Verdict: buildpolicy.VerdictAllow, Rationale: "toolchain configuration"}, nil, nil
	}
}

func TestFreeFormRefusedHandoffContinuesTheSession(t *testing.T) {
	h := freeFormTestAgent(t, []string{transcribedCall, check, handoffCall, modelSays("handed off"),
		// The follow-up turn builds the file.
		trial, check, handoffCall, done}, formalized)
	h.a.reviewer = rejecting(buildpolicy.RuleTranscribe, "cat > /src/index.js")
	got, _ := h.propose(t).Strategy()
	if diff := cmp.Diff(wantWorkflow(testLocation), got); diff != "" {
		t.Errorf("strategy mismatch (-want +got):\n%s", diff)
	}
	if n := len(h.model.requests); n != 8 {
		t.Fatalf("%d model calls, want 8 across the two turns", n)
	}
	followUp := h.model.request(4)
	for _, want := range []string{
		"proposal refused: the script supplies artifact content instead of building it",
		"F1 writing content into a file that ships in the artifact",
		`cat \u003e /src/index.js`, // JSON spells > as \u003e
		"The handoff is refused and was not formalized. Continue in this session",
	} {
		if !strings.Contains(followUp, want) {
			t.Errorf("the follow-up lacks %q:\n%s", want, followUp)
		}
	}
	if len(h.a.refusals) != 1 || h.a.refusals[0].Rule != "F1" {
		t.Errorf("refusals = %+v, want the one F1", h.a.refusals)
	}
	// The rejected handoff review is on file.
	var rejected bool
	for name, b := range h.records {
		if strings.HasSuffix(name, "-handoff-review.json") && bytes.Contains(b, []byte(`"reject"`)) && bytes.Contains(b, []byte("npm run build")) {
			rejected = true
		}
	}
	if !rejected {
		t.Errorf("no rejected handoff review among %v", h.records)
	}
	// The tree was reset to the ref, and only the commands after the refusal are
	// formalized.
	if n := len(h.execs); n != 6 || !strings.Contains(h.execs[3].Cmd[2], "git checkout --force "+testLocation.Ref+" && git clean -fd") {
		t.Errorf("execs = %d, want the prepare, the trial, the check, the restore, the trial and the check", n)
	}
	if len(h.prompts) != 1 || !strings.Contains(h.prompts[0], "## 1. run_in_container $ npm run build && npm pack") || strings.Contains(h.prompts[0], "cat > /src/index.js") {
		t.Errorf("formalizations = %d, prompt:\n%.900s", len(h.prompts), strings.Join(h.prompts, "\n"))
	}
	// The candidate was judged before it ran: doIteration's Check passes it.
	if refusal, err := h.a.Check(context.Background(), &ProposeOpts{}, h.a.ff.checked); refusal != "" || err != nil || h.a.ff.checked == nil {
		t.Errorf("Check(judged candidate) = %q, %v", refusal, err)
	}
}

func TestFreeFormHandoffRefusedRepeatedlyEndsTheSession(t *testing.T) {
	turn := []string{transcribedCall, check, handoffCall, modelSays("handed off")}
	h := freeFormTestAgent(t, append(append(append([]string{}, turn...), turn...), turn...), formalized)
	h.a.reviewer = rejecting(buildpolicy.RuleTranscribe, "cat > /src/index.js")
	if _, err := h.a.Propose(context.Background(), &ProposeOpts{}); err == nil || !strings.Contains(err.Error(), "handoff refused 3 times") {
		t.Errorf("err = %v, want the session given up", err)
	}
	if len(h.model.requests) != 12 || len(h.prompts) != 0 || h.a.ff.pending != nil {
		t.Errorf("%d model calls, %d formalizations, ff %+v; want three turns and no formalization", len(h.model.requests), len(h.prompts), h.a.ff.pending)
	}
}

// transcribing formalizes by writing the shipped file from a literal.
var transcribing = `{"deps": [{"runs": "npm install"}], "build": [{"runs": ` + jsonString("cat > index.js <<'EOF'\n"+strings.Repeat("module.exports = {};\n", 30)+"EOF\nnpm pack") + `}], "output_path": "lodash-4.17.21.tgz"}`

func TestFreeFormRefusedFormalizationIsRetoldInThePropose(t *testing.T) {
	h := freeFormTestAgent(t, freeFormScript, transcribing, formalized)
	h.a.reviewer = rejecting(buildpolicy.RuleTranscribe, "cat > index.js")
	s := h.propose(t)
	if got, _ := s.Strategy(); cmp.Diff(wantWorkflow(testLocation), got) != "" {
		t.Errorf("strategy = %+v, want the second formalization", got)
	}
	// The refusal cost no build, so the retry is in the same Propose.
	if len(h.model.requests) != 4 || len(h.prompts) != 2 || !strings.Contains(h.prompts[1], "## Candidate 1\n```\nrefused before any build: proposal refused") || !strings.Contains(h.prompts[1], "F1 writing content") {
		t.Errorf("model calls = %d, formalizations = %d, the re-formalization:\n%s", len(h.model.requests), len(h.prompts), h.prompts[len(h.prompts)-1])
	}
	if h.a.ff.pending.attempts != 2 || len(h.a.ff.pending.failures) != 1 || !strings.HasPrefix(h.a.ff.pending.failures[0], "refused before any build: proposal refused") {
		t.Errorf("attempts = %d, failures = %q", h.a.ff.pending.attempts, h.a.ff.pending.failures)
	}
	if fr := h.record(t, "-formalize-1.json"); !bytes.Contains(fr["error"], []byte("refused before any build")) || !bytes.Contains(fr["strategy"], []byte(`"flow"`)) {
		t.Errorf("formalize-1 record = %v, want the strategy and the refusal", fr)
	}
	if fr := h.record(t, "-formalize-2.json"); fr["error"] != nil || !bytes.Contains(fr["strategy"], []byte(`"flow"`)) {
		t.Errorf("formalize-2 record = %v, want the strategy", fr)
	}
	if h.a.ff.checked != s {
		t.Error("the accepted candidate was not marked judged")
	}
}

func TestFreeFormReviewRefusalIsRetriedInThePropose(t *testing.T) {
	// The first definition edits a shipped file. The second adds a command the
	// handoff did not run, so it is reviewed, not carried.
	edited := `{"deps": [{"runs": "npm install"}, {"runs": "npm install -g pnpm@9"}], "build": [{"runs": "sed -i 's/a/b/' index.js\nnpm run build\nnpm pack"}], "requires": {"system_deps": ["git"]}, "output_path": "lodash-4.17.21.tgz"}`
	linted := `{"deps": [{"runs": "npm install"}, {"runs": "npm install -g pnpm@9"}], "build": [{"runs": "npm run lint\nnpm run build\nnpm pack"}], "requires": {"system_deps": ["git"]}, "output_path": "lodash-4.17.21.tgz"}`
	h := freeFormTestAgent(t, freeFormScript, edited, linted)
	var seen []buildpolicy.ReviewInput
	h.a.reviewer = func(_ context.Context, in buildpolicy.ReviewInput) (*buildpolicy.Review, *genai.GenerateContentResponseUsageMetadata, error) {
		seen = append(seen, in)
		if strings.Contains(in.Build, "sed -i") {
			return &buildpolicy.Review{Verdict: buildpolicy.VerdictReject, Violations: []buildpolicy.Violation{{Rule: buildpolicy.RuleEdit, Line: "sed -i 's/a/b/' index.js", Why: "edits a shipped file to match"}}, Rationale: "supplied the answer"}, nil, nil
		}
		return &buildpolicy.Review{Verdict: buildpolicy.VerdictAllow, Rationale: "toolchain"}, nil, nil
	}
	s := h.propose(t)
	got, _ := s.Strategy()
	if ws, ok := got.(*rebuild.WorkflowStrategy); !ok || len(ws.Build) != 1 || ws.Build[0].Runs != "npm run lint\nnpm run build\nnpm pack" {
		t.Errorf("strategy = %+v, want the second formalization", got)
	}
	// The reviewer saw the handoff, the edited definition, and the second.
	if len(seen) != 3 || seen[0].Transcript == "" || !strings.Contains(seen[1].Build, "sed -i") || !strings.Contains(seen[2].Build, "npm run lint") {
		t.Fatalf("the reviewer saw %d proposals: %+v", len(seen), seen)
	}
	if len(h.model.requests) != 4 || len(h.prompts) != 2 || !strings.Contains(h.prompts[1], "## Candidate 1\n```\nrefused before any build: proposal refused") || !strings.Contains(h.prompts[1], "F4 editing the content of a file that ships in the artifact\n  sed -i 's/a/b/' index.js\n  edits a shipped file to match") {
		t.Errorf("model calls = %d, formalizations = %d, the re-formalization:\n%s", len(h.model.requests), len(h.prompts), h.prompts[len(h.prompts)-1])
	}
	if fr := h.record(t, "-formalize-1.json"); !bytes.Contains(fr["error"], []byte("refused before any build: proposal refused")) || !bytes.Contains(fr["strategy"], []byte("sed -i")) {
		t.Errorf("formalize-1 record = %v, want the refused strategy and the refusal", fr)
	}
	if fr := h.record(t, "-formalize-2.json"); fr["error"] != nil || !bytes.Contains(fr["strategy"], []byte("npm run lint")) {
		t.Errorf("formalize-2 record = %v, want the accepted strategy", fr)
	}
	var verdicts []string
	for _, r := range h.candidateReviews(t) {
		verdicts = append(verdicts, string(r["verdict"]))
	}
	slices.Sort(verdicts)
	if strings.Join(verdicts, ",") != `"allow","reject"` {
		t.Errorf("candidate review verdicts = %v, want the rejection and the allow on file", verdicts)
	}
	if len(h.a.refusals) != 1 || h.a.refusals[0].Rule != buildpolicy.RuleEdit || h.a.ff.checked != s || h.a.ff.pending.attempts != 2 {
		t.Errorf("refusals = %+v, checked = %v, attempts = %d", h.a.refusals, h.a.ff.checked == s, h.a.ff.pending.attempts)
	}
}

// The fallback runs the handoff's allowed commands, so it carries the verdict.
func TestFreeFormRefusedFormalizationsFallBackToTheHandoff(t *testing.T) {
	h := freeFormTestAgent(t, freeFormScript, transcribing, transcribing, transcribing)
	h.a.reviewer = rejecting(buildpolicy.RuleTranscribe, "cat > index.js")
	dir, _ := url.Parse("gs://sessions/s1/messages/3")
	s, err := h.a.Propose(context.Background(), &ProposeOpts{ChatUploadURL: dir})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	got, _ := s.Strategy()
	if _, ok := got.(*rebuild.ManualStrategy); !ok || len(h.model.requests) != 4 || len(h.prompts) != 3 {
		t.Errorf("proposal = %T after %d model calls and %d formalizations; want the handoff's commands after three refusals in one session", got, len(h.model.requests), len(h.prompts))
	}
	if ff := h.a.ff.pending; ff == nil || ff.attempts != 3 || !ff.fallback || len(ff.failures) != 3 || !strings.HasPrefix(ff.failures[2], "refused before any build") {
		t.Errorf("ff = %+v, want three refused formalizations and the fallback proposed", ff)
	}
	if h.a.ff.checked != s {
		t.Error("the fallback was not marked judged")
	}
	var n int
	for name := range h.records {
		if strings.Contains(name, "-formalize-") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("formalize records = %d, want 3", n)
	}
}

func TestFreeFormRefusedFallbackStartsOver(t *testing.T) {
	// The handoff passed review, and the fallback of its since-edited commands
	// checks out another commit, so it is refused and a fresh session starts.
	h := freeFormTestAgent(t, append(append([]string{}, freeFormScript...), freeFormScript...), formalized, formalized, formalized, formalized)
	h.a.reviewer = rejecting(buildpolicy.RuleCopy, "git checkout 7d18bf46")
	h.propose(t)
	for range 1 + formalizeRetries {
		h.fail("rebuild content mismatch")
		h.propose(t)
	}
	h.a.ff.pending.handoff.BuildCommands.Build = "git checkout 7d18bf46b662f1c6f806c1f8d3ea6b9c5b88bbdb\nnpm pack"
	h.fail("rebuild content mismatch")
	got, _ := h.propose(t).Strategy()
	if _, ok := got.(*rebuild.WorkflowStrategy); !ok || len(h.model.requests) != 8 || h.a.ff.pending.attempts != 1 {
		t.Errorf("after the refused fallback: %T, %d model calls, attempts %d; want a fresh session's formalization", got, len(h.model.requests), h.a.ff.pending.attempts)
	}
}

func TestTranscript(t *testing.T) {
	trace := []toolCall{
		{Tool: "run_on_host", Args: json.RawMessage(`{"command":"tar -xzf /home/builder/upstream/x.tgz -C /tmp/up && diff /tmp/up/package/a builds/iter-2/src/a"}`)},
		{Tool: "run_in_container", Args: json.RawMessage(`{"command":"npm pack"}`), Output: "built"},
		{Tool: "check_artifact", Args: json.RawMessage(`{"path":"/src/x.tgz"}`)},
		{Tool: "read_repo_file", Args: json.RawMessage(`{"path":"package.json"}`)},
		{Tool: "read_logs_end", Args: json.RawMessage(`{}`)},
		{Tool: "handoff", Args: json.RawMessage(handoffArgs)},
	}
	want := "tar -xzf /upstream/x.tgz -C /tmp/up && diff /tmp/up/package/a /src/a\n" +
		"npm pack\n" +
		"# read_repo_file package.json\n" +
		"# read_logs_end\n"
	if diff := cmp.Diff(want, transcript(trace)); diff != "" {
		t.Errorf("transcript mismatch (-want +got):\n%s", diff)
	}
}

func TestFreeFormCarriesTheHandoffVerdict(t *testing.T) {
	// The first formalization splits the deps into commented steps, which still
	// carry the verdict. The second adds a command.
	commented := `{"deps": [{"runs": "# the attempt's deps\nnpm install"}, {"runs": "npm install -g pnpm@9  # the handoff's"}], "build": [{"runs": "npm run build\nnpm pack"}], "requires": {"system_deps": ["git"]}, "output_path": "lodash-4.17.21.tgz"}`
	extra := `{"deps": [{"runs": "npm install"}, {"runs": "npm install -g pnpm@9"}], "build": [{"runs": "npm run lint\nnpm run build\nnpm pack"}], "requires": {"system_deps": ["git"]}, "output_path": "lodash-4.17.21.tgz"}`
	h := freeFormTestAgent(t, freeFormScript, commented, extra)
	var seen []buildpolicy.ReviewInput
	h.a.reviewer = allowing(&seen)
	s := h.propose(t)
	if len(seen) != 1 || seen[0].Transcript == "" {
		t.Fatalf("the reviewer saw %d proposals, want the handoff alone", len(seen))
	}
	var from string
	for name := range h.records {
		if strings.HasSuffix(name, "-handoff-review.json") {
			from = name
		}
	}
	reviews := h.candidateReviews(t)
	if len(reviews) != 1 {
		t.Fatalf("candidate reviews = %v, want the carried one", reviews)
	}
	for name, r := range reviews {
		if string(r["carried_from"]) != jsonString(from) || !bytes.Contains(r["review"], []byte(`"allow"`)) {
			t.Errorf("%s = %v, want the verdict carried from %s", name, r, from)
		}
	}
	if h.a.ff.checked != s {
		t.Error("the carried candidate was not marked judged")
	}
	// A candidate with a command the handoff did not run is reviewed.
	h.fail("rebuild content mismatch")
	h.propose(t)
	if len(seen) != 2 || !strings.Contains(seen[1].Build, "npm run lint") {
		t.Fatalf("the reviewer saw %d proposals, want the extra candidate as the second", len(seen))
	}
	reviews = h.candidateReviews(t)
	if len(reviews) != 2 {
		t.Fatalf("candidate reviews = %v, want two", reviews)
	}
	for name, r := range reviews {
		if _, carried := r["carried_from"]; !carried && !bytes.Contains(r["verdict"], []byte("allow")) {
			t.Errorf("%s = %v, want the reviewer's verdict", name, r)
		}
	}
}

func TestFreeFormCarries(t *testing.T) {
	ff := &freeForm{
		exemplar: rebuild.Instructions{Source: "git clone https://github.com/lodash/lodash .\ngit checkout --force 'aa1d'", Deps: "wget node.tgz | tar xzf - -C /usr/local/\nnpm install", Build: "npm pack"},
		handoff:  handoff{BuildCommands: buildCommands{Deps: "npm install\nnpm i --no-save rollup@4.18.0", Build: "cat << 'EOF' > node_modules/x/options.js\n  a: 1\nEOF\nnpm run prepare\nnpm pack"}},
	}
	src, build := ff.exemplar.Source, ff.handoff.BuildCommands.Build
	for _, tc := range []struct {
		name string
		inst rebuild.Instructions
		want bool
	}{
		{"the handoff on the exemplar's deps", rebuild.Instructions{Source: src, Deps: "wget node.tgz | tar xzf - -C /usr/local/\nnpm install\nnpm i --no-save rollup@4.18.0", Build: build}, true},
		{"split into commented steps", rebuild.Instructions{Source: src, Deps: "# toolchain\nwget   node.tgz | tar xzf - -C /usr/local/\n\nnpm install\n# the handoff's pins\nnpm i --no-save rollup@4.18.0", Build: build + "  # as handed off"}, true},
		{"the exemplar's toolchain step dropped", rebuild.Instructions{Source: src, Deps: "npm install\nnpm i --no-save rollup@4.18.0", Build: build}, true},
		{"a command added", rebuild.Instructions{Source: src, Deps: "npm install\nnpm i --no-save rollup@4.18.0", Build: "sed -i 's/a/b/' index.js\n" + build}, false},
		{"a command changed", rebuild.Instructions{Source: src, Deps: "npm install\nnpm i --no-save rollup@4.19.0", Build: build}, false},
		{"a command dropped", rebuild.Instructions{Source: src, Deps: "npm install\nnpm i --no-save rollup@4.18.0", Build: strings.Replace(build, "npm run prepare\n", "", 1)}, false},
		{"reordered", rebuild.Instructions{Source: src, Deps: "npm install\nnpm i --no-save rollup@4.18.0", Build: "npm run prepare\ncat << 'EOF' > node_modules/x/options.js\n  a: 1\nEOF\nnpm pack"}, false},
		{"its own source step", rebuild.Instructions{Source: "git clone https://github.com/lodash/lodash .\ngit checkout --force v4.17.21", Deps: "npm install\nnpm i --no-save rollup@4.18.0", Build: build}, false},
	} {
		if got := ff.carries(tc.inst); got != tc.want {
			t.Errorf("%s: carries = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The give-up record is filed beside the transcript, with the job's own
// closest candidate.
func TestFreeFormGiveUpEndsTheSession(t *testing.T) {
	mismatch := func(entries int, path string) checkResult {
		return checkResult{code: 1, out: "ab cd /tmp/check/candidate.stabilized " + stabilizedUpstream + fmt.Sprintf("\nwithin candidate (vs upstream):\n  differ (%d):\n    %s\n", entries, path)}
	}
	h := freeFormTestAgent(t, []string{
		modelCalls("check_artifact", `{"path":"/src/a.tgz"}`),
		modelCalls("check_artifact", `{"path":"/src/b.tgz"}`),
		modelCalls("give_up", `{"reason":"the locale data comes from a CLDR build that is gone","closest":"/src/b.tgz","build_commands":{"deps":"npm ci","build":"npm pack"},"remaining":"locale/*.json: generated from CLDR 41, which is no longer published"}`),
		modelSays("Giving up."),
	}, formalized)
	h.checks = []checkResult{mismatch(5, "package/index.js"), mismatch(2, "package/locale/en.json")}
	dir, _ := url.Parse("gs://sessions/s1/messages/2")
	_, err := h.a.Propose(context.Background(), &ProposeOpts{ChatUploadURL: dir})
	var gu gaveUp
	if !errors.As(err, &gu) || !strings.Contains(gu.Reason, "CLDR build") {
		t.Fatalf("Propose = %v, want the give-up with its reason", err)
	}
	if gu.Closest == nil || gu.Closest.Entries != 2 || gu.Closest.Path != "/src/b.tgz" {
		t.Errorf("job closest = %+v, want the 2-entry check of /src/b.tgz", gu.Closest)
	}
	r := h.record(t, "-giveup.json")
	for key, want := range map[string]string{"reason": "CLDR build", "closest": "/src/b.tgz", "build_commands": "npm pack", "remaining": "CLDR 41", "job_closest": `"entries":2`} {
		if !bytes.Contains(r[key], []byte(want)) {
			t.Errorf("give-up record %s = %s, want it to contain %q", key, r[key], want)
		}
	}
	if len(h.prompts) != 0 || h.a.ff.pending != nil {
		t.Error("a session that gave up was formalized")
	}
}

// A farther candidate does not displace the closest.
func TestArtifactCheckReportsTheClosest(t *testing.T) {
	var k artifactCheck
	if got := k.note(schema.AgentCandidate{Path: "a", Entries: 4}); !strings.Contains(got, "4 differing entries (a)") {
		t.Errorf("first note = %q", got)
	}
	k.note(schema.AgentCandidate{Path: "b", Entries: 1})
	if got := k.note(schema.AgentCandidate{Path: "c", Entries: 3}); !strings.Contains(got, "1 differing entries (b)") {
		t.Errorf("note after a farther candidate = %q, want b to stay closest", got)
	}
}

func TestFreeFormWindDownNotice(t *testing.T) {
	h := freeFormTestAgent(t, []string{
		modelCalls("check_artifact", `{"path":"/src/a.tgz"}`),
		modelCalls("give_up", `{"reason":"out of time","closest":"/src/a.tgz"}`),
		modelSays("Done."),
	}, formalized)
	h.checks = []checkResult{vmMismatch}
	h.a.ff.windDown = time.Now().Add(-time.Second)
	dir, _ := url.Parse("gs://sessions/s1/messages/2")
	if _, err := h.a.Propose(context.Background(), &ProposeOpts{ChatUploadURL: dir}); !errors.As(err, new(gaveUp)) {
		t.Fatalf("Propose = %v, want the model's give-up", err)
	}
	var noticed bool
	for _, b := range h.records {
		noticed = noticed || bytes.Contains(b, []byte("time budget is spent"))
	}
	if !noticed {
		t.Error("no tool reply past the wind-down carried the notice")
	}
}

// Past the grace, the job gives up for the model and files the record.
func TestFreeFormWindDownEndsTheTurn(t *testing.T) {
	h := freeFormTestAgent(t, []string{modelSays("still working")}, formalized)
	h.a.ff.windDown = time.Now().Add(-windDownGrace - time.Second)
	dir, _ := url.Parse("gs://sessions/s1/messages/2")
	_, err := h.a.Propose(context.Background(), &ProposeOpts{ChatUploadURL: dir})
	var gu gaveUp
	if !errors.As(err, &gu) || gu.Reason != timeRanOut {
		t.Fatalf("Propose = %v, want the job's give-up", err)
	}
	if r := h.record(t, "-giveup.json"); !bytes.Contains(r["reason"], []byte("ran out")) {
		t.Errorf("give-up record = %v", r)
	}
}
