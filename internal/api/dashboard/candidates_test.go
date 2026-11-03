// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestLastCommand(t *testing.T) {
	for _, tc := range []struct{ cmd, want string }{
		{"npm run build:prod && npm run build:types", "npm run build:types"},
		{"cd /src && git checkout -- . && git clean -fdx && cargo package --no-verify", "cargo package --no-verify"},
		{"set -e\nnpm ci\nnpm pack", "npm pack"},
		// A separator inside quotes does not end a command, so a command
		// that ends in a script is named whole.
		{"npm ci && node -e 'const x = 1; console.log(x)'", "node -e 'const x = 1; console.log(x)'"},
		{`npx cross-env NODE_ENV=production node -e "a; b"`, `npx cross-env NODE_ENV=production node -e "a; b"`},
		{"rm -f p-*.tgz && /usr/bin/npx --package=npm@6.14.5 -c 'npm pack'", "/usr/bin/npx --package=npm@6.14.5 -c 'npm pack'"},
		{`echo "it's" && npm pack`, "npm pack"},
		// Redirections and continuations are not separators either, and a
		// command's redirections and trailing newline are not part of its name.
		{"npm run build 2>&1 | tail -5", "tail -5"},
		{"npm pack 2>&1", "npm pack"},
		// A command inside a subshell or a loop is still a command, and one
		// inside a substitution is part of the command around it.
		{"cd /src && (npm ci && npm pack)", "npm pack"},
		{"for d in a b; do (cd $d && npm pack); done", "npm pack"},
		{"cp $(npm pack --silent) /out", "cp $(npm pack --silent) /out"},
		{"npm pack \\\n  --dry-run", "npm pack \\\n  --dry-run"},
		{"npm ci && \\\n  npm pack\n", "npm pack"},
	} {
		if got := lastCommand(tc.cmd); got != tc.want {
			t.Errorf("lastCommand(%q) = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

func TestParseDiffSummary(t *testing.T) {
	text := "rebuild content mismatch\n\nFile-level diff summary of the stabilized artifacts:\nwithin rebuild (vs upstream):\n  only in upstream (2):\n    package/dist/a.js\n    package/index.d.ts\n  differ (1):\n    package/package.json (Line endings differ (-CRLF,+mixed); Diff shown with normalized line endings)\n  within package/inner.tgz:\n    only in rebuild (1):\n      x.txt\n\nThe build produced different bytes."
	want := []DiffGroup{
		{Label: "only in upstream (2)", Kind: "upstream", Mark: "+", Entries: []DiffEntry{{"package/dist/a.js", ""}, {"package/index.d.ts", ""}}},
		{Label: "differ (1)", Kind: "differ", Mark: "≠", Entries: []DiffEntry{{"package/package.json", "Line endings differ (-CRLF,+mixed); Diff shown with normalized line endings"}}},
		{Label: "package/inner.tgz: only in rebuild (1)", Kind: "candidate", Mark: "−", Entries: []DiffEntry{{"x.txt", ""}}},
	}
	if diff := cmp.Diff(want, parseDiffSummary(text)); diff != "" {
		t.Errorf("parseDiffSummary diff (-want +got):\n%s", diff)
	}
}

func TestCheckComparison(t *testing.T) {
	at := time.Date(2026, 10, 19, 4, 5, 22, 0, time.UTC)
	got := checkComparison(at, map[string]any{
		"match":  false,
		"result": "MISMATCH: stabilized sha256 differ (candidate 80f171729a02d40d, upstream cd021c12314faa24). 2 differing entries:\nwithin candidate (vs upstream):\n  differ (2):\n    package/a.js (WARNING: Files have mixed line endings which are not shown in diff)\n    package/b.js\nStabilized forms: /tmp/tmp.X/candidate.stabilized and /home/builder/upstream/p.tgz.stabilized; tools/diffr on them shows content hunks.\nClosest so far in this session: 2 differing entries (/src/p.tgz).",
	})
	want := &Comparison{
		By: "check_artifact", At: at, Entries: 2, Candidate: "80f17172", Upstream: "cd021c12",
		Groups: []DiffGroup{{Label: "differ (2)", Kind: "differ", Mark: "≠", Entries: []DiffEntry{{"package/a.js", "WARNING: Files have mixed line endings which are not shown in diff"}, {"package/b.js", ""}}}},
		Note:   "Closest so far: 2 entries.",
		forms:  "/tmp/tmp.X/candidate.stabilized",
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(Comparison{})); diff != "" {
		t.Errorf("checkComparison diff (-want +got):\n%s", diff)
	}
	if c := checkComparison(at, map[string]any{"error": "/src/p.tgz: no such file on the host"}); c.Err == "" || c.Chip() != "Not compared" {
		t.Errorf("checkComparison(error) = %+v", c)
	}
}

// TestDeriveCandidates walks a free-form session: the seed build that
// mismatched, a build that timed out on the way to a pack checked off by one,
// a match the review refused, and an accepted match the job verified.
func TestDeriveCandidates(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)
	at := func(m, s int) time.Time { return t0.Add(time.Duration(m)*time.Minute + time.Duration(s)*time.Second) }
	call := func(when time.Time, tool string, args map[string]any) TranscriptEvent {
		return TranscriptEvent{Kind: "call", At: when, Tool: tool, Args: args}
	}
	shell := func(when time.Time, tool, command string, purpose schema.Purpose) TranscriptEvent {
		return TranscriptEvent{Kind: "call", At: when, Tool: tool, Args: map[string]any{"command": command}, Command: command, Purpose: purpose, All: []schema.Purpose{purpose}}
	}
	resp := func(when time.Time, tool string, r map[string]any) TranscriptEvent {
		return TranscriptEvent{Kind: "response", At: when, Tool: tool, Response: r}
	}
	step := func(label string, exit int, from, to time.Time) BuildStep {
		return BuildStep{Label: label, State: schema.ScratchExecCompleted, ExitCode: exit, Started: from, Finished: to}
	}
	seed := &BuildAttemptView{ID: "iter-1", Iter: 1, At: at(0, 10), Steps: []BuildStep{
		step("prepare", 0, at(0, 10), at(0, 11)), step("start", 0, at(0, 11), at(0, 12)),
		step("phase/setup", 0, at(0, 12), at(0, 20)), step("phase/source", 0, at(0, 20), at(0, 25)), step("phase/build", 0, at(0, 25), at(0, 50)),
		step("artifact", 0, at(0, 51), at(0, 52)), step("compare", 1, at(0, 52), at(0, 53)),
	}}
	verify := &BuildAttemptView{ID: "iter-2", Iter: 2, At: at(20, 0), Steps: []BuildStep{
		step("prepare", 0, at(20, 0), at(20, 1)), step("phase/build", 0, at(20, 1), at(25, 0)), step("artifact", 0, at(25, 0), at(25, 1)),
	}}
	match := map[string]any{"match": true, "result": "MATCH: the stabilized candidate equals the stabilized upstream (sha256 cd021c12314faa24)."}
	data := &SessionData{
		Session: NewSessionView(schema.AgentSession{StopReason: schema.AgentCompleteReasonSuccess, Summary: "Build successful"}),
		Trajectory: []IterationGroup{{
			Number: 1,
			Record: &schema.AgentIteration{Result: &schema.AgentBuildResult{ErrorMessage: "rebuild content mismatch\nwithin rebuild (vs upstream):\n  only in upstream (1):\n    package/dist/a.js\n"}},
			Events: []TranscriptEvent{
				{Kind: "proposal", Title: "inference proposal"},
				{Kind: "build", At: at(0, 10), Build: seed},
			},
		}, {
			Number: 2,
			Events: []TranscriptEvent{
				shell(at(1, 0), "run_in_container", "npm ci", schema.PurposeDeps),
				resp(at(1, 30), "run_in_container", map[string]any{"exit_code": 0.0}),
				shell(at(2, 0), "run_in_container", "npm run build", schema.PurposeBuild),
				resp(at(7, 0), "run_in_container", map[string]any{"error": "command timed out after 300s"}),
				shell(at(8, 0), "run_in_container", "rollup -c", schema.PurposeBuild),
				resp(at(8, 30), "run_in_container", map[string]any{"exit_code": 0.0}),
				shell(at(8, 40), "run_in_container", "npm pack", schema.PurposePackage),
				resp(at(8, 45), "run_in_container", map[string]any{"exit_code": 0.0}),
				call(at(9, 0), "check_artifact", map[string]any{"path": "/src/p.tgz"}),
				resp(at(9, 5), "check_artifact", map[string]any{"match": false, "result": "MISMATCH: stabilized sha256 differ (candidate 80f171729a02d40d, upstream cd021c12314faa24). 1 differing entries:\nwithin candidate (vs upstream):\n  differ (1):\n    package/index.d.ts\nStabilized forms: /tmp/tmp.X/candidate.stabilized and /u.stabilized; tools/diffr on them shows content hunks."}),
				shell(at(9, 30), "run_on_host", "tools/diffr /tmp/tmp.X/candidate.stabilized /u.stabilized", schema.PurposeCompare),
				resp(at(9, 31), "run_on_host", map[string]any{"exit_code": 1.0}),
				shell(at(10, 0), "run_in_container", "npm pack", schema.PurposePackage),
				resp(at(10, 5), "run_in_container", map[string]any{"exit_code": 0.0}),
				call(at(10, 10), "check_artifact", map[string]any{"path": "/src/p.tgz"}),
				resp(at(10, 12), "check_artifact", match),
				call(at(11, 0), "handoff", map[string]any{"summary": "first try"}),
				{Kind: "review", At: at(11, 10), Review: &ReviewView{Verdict: "reject", Findings: []Finding{{Rule: "F1"}, {Rule: "F4"}}}},
				shell(at(12, 0), "run_in_container", "npm pack", schema.PurposePackage),
				resp(at(12, 5), "run_in_container", map[string]any{"exit_code": 0.0}),
				call(at(12, 10), "check_artifact", map[string]any{"path": "/src/p.tgz"}),
				resp(at(12, 12), "check_artifact", match),
				call(at(13, 0), "handoff", map[string]any{"summary": "second try"}),
				{Kind: "review", At: at(13, 10), Review: &ReviewView{Verdict: "declare"}},
				{Kind: "review", At: at(13, 11), Review: &ReviewView{Verdict: "declare", Carried: true}},
				{Kind: "build", At: at(20, 0), Build: verify},
			},
		}},
	}
	deriveCandidates(data, t0, at(30, 0))
	type row struct {
		Change string
		Stages string
		Took   string
	}
	var got []row
	for _, c := range data.Candidates {
		var st string
		for _, s := range c.Stages {
			st += s.State + ":" + s.Label + " "
		}
		got = append(got, row{c.Change, st, c.Took})
	}
	want := []row{
		{"Seed build from the inference proposal", "ok:✓ ok:✓ ok:✓ ok:✓ off:1 none:– none:– ", "43s"},
		{"rollup -c && npm pack", "reused:✓ ok:✓ ok:✓ ok:✓ off:1 none:– none:– ", "5m 35s"},
		{"npm pack", "reused:✓ reused:✓ ok:✓ ok:✓ match:= failed:F1 none:– ", "5s"},
		{"npm pack", "reused:✓ reused:✓ ok:✓ ok:✓ match:= ok:declare ok:✓ ", "5s"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("candidates diff (-want +got):\n%s", diff)
	}
	c := data.Candidates
	if d := c[1].Compare.Diffr; d == nil || d.At != at(9, 30) {
		t.Errorf("candidate 2's diffr = %+v, want the host call at 09:30", d)
	}
	if got, want := c[2].Outcome(), "Matched, but the review refused it: F1, F4"; got != want {
		t.Errorf("Outcome() = %q, want %q", got, want)
	}
	if got := len(c[3].After); got != 3 { // handoff, review, and job build, without the carried review
		t.Errorf("candidate 4 has %d calls after it, want 3: %+v", got, c[3].After)
	}
	if v := data.Verdict; v.Kind != "reproduced" || v.Accepted != c[3] || v.Matched != nil || v.Closest != c[1] || v.Line != "The job reproduced candidate 4" {
		t.Errorf("Verdict = %+v", v)
	}
	totals := map[string]string{}
	for _, tot := range data.Strip.Totals {
		totals[tot.Kind] = tot.Took
	}
	if diff := cmp.Diff(map[string]string{"setup": "1m 00s", "timeout": "5m 00s", "build": "45s", "verify": "10m 00s", "other": "13m 15s"}, totals); diff != "" {
		t.Errorf("strip totals diff (-want +got):\n%s", diff)
	}
}

// TestFailedCallStages runs container calls up to one candidate. A failed
// call fails only the first stage of its primary purpose, and a call that
// succeeds marks every stage its purposes name. Calls without a purpose open
// no candidate.
func TestFailedCallStages(t *testing.T) {
	type run struct {
		cmd  string
		all  []schema.Purpose // the primary purpose first
		exit float64
	}
	pkg, deps, src := schema.PurposePackage, schema.PurposeDeps, schema.PurposeSource
	for _, tc := range []struct {
		runs []run
		want string
	}{
		{[]run{{"cd /src && git checkout -- . && git clean -fdx && cargo package --no-verify", []schema.Purpose{pkg, src}, 101}}, "reused:✓ reused:✓ failed:✕ none:– none:– none:– none:– "},
		{[]run{{"git checkout v2 && npm ci", []schema.Purpose{deps, src}, 1}, {"npm pack", []schema.Purpose{pkg}, 0}}, "reused:✓ failed:✕ ok:✓ ok:✓ none:– none:– none:– "},
		{[]run{{"git checkout v2 && npm ci && npm pack", []schema.Purpose{pkg, deps, src}, 0}}, "ok:✓ ok:✓ ok:✓ ok:✓ none:– none:– none:– "},
		{[]run{{"npm ci", nil, 0}, {"npm pack", nil, 0}}, ""},
	} {
		t0 := time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)
		var events []TranscriptEvent
		for i, r := range tc.runs {
			at := t0.Add(time.Duration(i) * time.Minute)
			call := TranscriptEvent{Kind: "call", At: at, Tool: "run_in_container", Command: r.cmd, All: r.all}
			if len(r.all) > 0 {
				call.Purpose = r.all[0]
			}
			events = append(events, call,
				TranscriptEvent{Kind: "response", At: at.Add(time.Second), Tool: "run_in_container", Response: map[string]any{"exit_code": r.exit}})
		}
		data := &SessionData{Trajectory: []IterationGroup{{Number: 2, Events: events}}}
		deriveCandidates(data, t0, t0.Add(time.Hour))
		if tc.want == "" {
			if len(data.Candidates) != 0 {
				t.Errorf("%v: %d candidates, want none", tc.runs, len(data.Candidates))
			}
			continue
		}
		if len(data.Candidates) != 1 {
			t.Errorf("%v: %d candidates, want 1", tc.runs, len(data.Candidates))
			continue
		}
		var got string
		for _, s := range data.Candidates[0].Stages {
			got += s.State + ":" + s.Label + " "
		}
		if got != tc.want {
			t.Errorf("%v: stages %q, want %q", tc.runs, got, tc.want)
		}
	}
}

func TestArgsLine(t *testing.T) {
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"ref": "v2.13.5", "reason": "the release tag"}, "v2.13.5 · reason: the release tag"},
		{map[string]any{"path": "pyproject.toml"}, "pyproject.toml"},
		{map[string]any{"b": 2.0, "a": "x"}, "a: x · b: 2"},
	} {
		if got := argsLine(tc.args); got != tc.want {
			t.Errorf("argsLine(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestSessionVerdict(t *testing.T) {
	for summary, want := range map[string]string{
		"Iteration budget (30m0s) spent after 2 builds":                                     "timed out",
		"Inference failed: generating strategy: inference ran past 15m0s: context deadline": "timed out",
		"Maximum iterations (5) reached":                                                    "resource limit",
		"Allocating scratch: rpc error: code = ResourceExhausted desc = admitting":          "resource limit",
		"Inference failed: generating strategy: AI-assisted inference failed: Error 500":    "error",
		"reaper: session exceeded its timeout without reporting completion":                 "timed out",
	} {
		if got := NewSessionView(schema.AgentSession{StopReason: schema.AgentCompleteReasonFailed, Summary: summary}).Verdict(); got != want {
			t.Errorf("Verdict(%q) = %q, want %q", summary, got, want)
		}
	}
	if got := NewSessionView(schema.AgentSession{StopReason: schema.AgentCompleteReasonThrottled}).Verdict(); got != "resource limit" {
		t.Errorf("Verdict(THROTTLED) = %q", got)
	}
}

// TestSessionWithoutCandidates renders a session that built nothing: the
// verdict row still says how it ended, and the page head carries no summary.
func TestSessionWithoutCandidates(t *testing.T) {
	sum := "Allocating scratch: rpc error: code = ResourceExhausted desc = admitting"
	s := NewSessionView(schema.AgentSession{ID: "sess-2", StopReason: schema.AgentCompleteReasonError, Summary: sum})
	var buf bytes.Buffer
	if err := SessionTmpl.Execute(&buf, &SessionData{Session: s, Verdict: Verdict{Kind: s.Verdict(), Line: sum, Summary: sum}}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"No candidate was built.", `<b class="tone-bad">Resource limit</b>`} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(out, "session-summary") {
		t.Error("page head still carries the summary")
	}
}
