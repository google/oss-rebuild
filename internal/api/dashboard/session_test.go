// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/api/option"
)

// sessionFixtureReader returns one canned session plus iterations,
// regardless of the request.
type sessionFixtureReader struct {
	session    schema.AgentSession
	iterations []schema.AgentIteration
}

func (f *sessionFixtureReader) FetchSessions(context.Context, *rundex.FetchSessionsReq) ([]schema.AgentSession, error) {
	return []schema.AgentSession{f.session}, nil
}
func (f *sessionFixtureReader) FetchIterations(context.Context, *rundex.FetchIterationsReq) ([]schema.AgentIteration, error) {
	return f.iterations, nil
}

func TestSessionHandler(t *testing.T) {
	ctx := context.Background()
	created := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	execs := db.NewMemoryScratchExecs()
	// A host exec with no matching call renders as a plain exec event.
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID:         "op-1",
		ScratchID:  "scr-1",
		Label:      schema.ToolLabel("run_on_host"),
		Cmd:        []string{"sh", "-c", "docker build ."},
		State:      schema.ScratchExecCompleted,
		OutURI:     "gs://out-bucket/obliv/op-1/out",
		CreatedAt:  created.Add(time.Minute),
		StartedAt:  created.Add(time.Minute),
		FinishedAt: created.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// The chat-less first build. Its iter-1 label materializes iteration 1.
	for i, e := range []schema.ScratchExec{{
		Label:    schema.ScratchLabelPrepare,
		Cmd:      []string{"/bin/sh", "-c", "set -eu\nmkdir -p \"/home/builder/builds/iter-1/out\"\ncat > \"/home/builder/builds/iter-1/build.sh\"\ndocker ps -aq --filter name=^rb- | xargs -r docker rm -f"},
		State:    schema.ScratchExecCompleted,
		ExitCode: 0,
	}, {
		Label:    schema.ScratchLabelStart,
		Cmd:      []string{"docker", "run", "--detach", "--name", "rb-iter-1", "img", "sleep", "infinity"},
		State:    schema.ScratchExecCompleted,
		ExitCode: 0,
	}, {
		Label:    schema.PhaseLabel(rebuild.PhaseBuild),
		Cmd:      []string{"docker", "exec", "rb-iter-1", "/bin/sh", "-c", "set -eux\nnpm install"},
		State:    schema.ScratchExecCompleted,
		ExitCode: 7,
	}} {
		e.ID = fmt.Sprintf("mach-%d", i)
		e.ScratchID = "scr-1"
		e.CreatedAt = created.Add(time.Duration(i) * time.Second)
		if err := execs.Insert(ctx, e); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	deps := &Deps{
		Sessions: &sessionFixtureReader{
			session: schema.AgentSession{
				ID:            "sess-1",
				Target:        rebuild.Target{Ecosystem: rebuild.NPM, Package: "lodash", Version: "4.17.21"},
				Status:        schema.AgentSessionStatusCompleted,
				StopReason:    schema.AgentCompleteReasonSuccess,
				ExecutionMode: schema.AgentExecutionModeScratch,
				ScratchID:     "scr-1",
				Model:         "gemini-test",
				Created:       created,
				Updated:       created.Add(10 * time.Minute),
				Usage:         &schema.TokenUsage{Input: 1000, CachedInput: 400, Output: 250},
			},
			iterations: []schema.AgentIteration{{
				SessionID:   "sess-1",
				Number:      3,
				ObliviousID: "obliv-3",
				Status:      schema.AgentIterationStatusSuccess,
				Result:      &schema.AgentBuildResult{BuildSuccess: true},
				Created:     created,
			}},
		},
		Execs: execs,
	}
	got, err := Session(ctx, SessionRequest{ID: "sess-1"}, deps)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	var buf bytes.Buffer
	if err := SessionTmpl.Execute(&buf, got); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"sess-1",
		"lodash",
		"Iteration 1",
		"Iteration 3",
		"build ok",
		"docker build .",
		"/session/sess-1/exec/op-1/out",
		"1K in (40% cached) / 250 out",
		"exec · tool/run_on_host · completed · exit 0", // trajectory event in iteration 3
		// One attempt under iteration 1, named by its failing phase.
		"build iter-1 · phase/build exit 7",
		"npm install",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	var iters []int
	for _, e := range got.Execs {
		iters = append(iters, e.Iter)
	}
	// ListByScratch orders by creation, so the build's execs come first.
	if want := []int{1, 1, 1, 3}; !slices.Equal(iters, want) {
		t.Errorf("exec iterations = %v; want %v", iters, want)
	}
}

func TestParseTranscriptName(t *testing.T) {
	when := time.Date(2026, 8, 19, 12, 0, 1, 500000000, time.UTC)
	for _, tc := range []struct {
		name string
		in   string
		want transcriptName
		ok   bool
	}{{
		name: "chat message",
		in:   "2/" + when.Format(time.RFC3339Nano) + "-1-model.json",
		want: transcriptName{Iter: 2, Time: when, Part: 1, Role: "model"},
		ok:   true,
	}, {
		name: "inference proposal",
		in:   "1/0-inference-proposal.json",
		want: transcriptName{Iter: 1, Proposal: true, Inference: true},
		ok:   true,
	}, {
		name: "llm proposal",
		in:   "3/0-proposal.json",
		want: transcriptName{Iter: 3, Proposal: true},
		ok:   true,
	}, {
		name: "whole seconds keep parsing despite RFC3339Nano trimming",
		in:   "4/2026-08-19T12:00:01Z-0-user.json",
		want: transcriptName{Iter: 4, Time: time.Date(2026, 8, 19, 12, 0, 1, 0, time.UTC), Part: 0, Role: "user"},
		ok:   true,
	}, {
		name: "non-json object",
		in:   "2/notes.txt",
		ok:   false,
	}, {
		name: "non-numeric iteration",
		in:   "latest/2026-08-19T12:00:01Z-0-user.json",
		ok:   false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTranscriptName(tc.in)
			if ok != tc.ok {
				t.Fatalf("parseTranscriptName(%q) ok = %v; want %v", tc.in, ok, tc.ok)
			}
			if !ok {
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parseTranscriptName(%q) (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

func TestEventsFrom(t *testing.T) {
	name := transcriptName{Iter: 2, Time: time.Date(2026, 8, 19, 12, 0, 1, 0, time.UTC), Part: 0, Role: "model"}
	body := []byte(`{
		"role": "model",
		"parts": [
			{"text": "internal reasoning", "thought": true},
			{"text": "Trying a pinned base image next."},
			{"functionCall": {"name": "run_in_container", "args": {"command": "pip install foo", "timeout_seconds": 300}}}
		]
	}`)
	events := eventsFrom(name, body)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if diff := cmp.Diff([]string{"thought", "model", "call"}, kinds); diff != "" {
		t.Fatalf("kinds (-want +got):\n%s", diff)
	}
	if events[2].Command != "pip install foo" {
		t.Errorf("call command = %q; want %q", events[2].Command, "pip install foo")
	}
	if !strings.Contains(events[2].Title, "timeout 300s") {
		t.Errorf("call title = %q; want timeout noted", events[2].Title)
	}

	resp := eventsFrom(transcriptName{Iter: 2, Role: "user"}, []byte(`{
		"role": "user",
		"parts": [{"functionResponse": {"name": "run_in_container", "response": {"output": "done", "exit_code": 0}}}]
	}`))
	if len(resp) != 1 || resp[0].Kind != "response" || resp[0].Body != "done" {
		t.Fatalf("response events = %+v; want one response with body \"done\"", resp)
	}
	if !strings.Contains(resp[0].Note, "exit 0") {
		t.Errorf("response note = %q; want exit code noted", resp[0].Note)
	}
}

// TestLoadTranscriptOrder serves a transcript from a fake GCS endpoint whose
// reads finish out of order and checks the events still land in chronological
// (time, part) order with every readable object present.
func TestLoadTranscriptOrder(t *testing.T) {
	const bucket = "sessions"
	const sessionID = "sess-1"
	objects := map[string][]byte{
		sessionID + "/messages/1/0-inference-proposal.json": []byte(`{}`),
		sessionID + "/messages/2/notes.txt":                 []byte(`not a message`),
	}
	var want []string
	// Two exchanges whose RFC3339Nano stamps sort lexicographically in the
	// wrong order: "12:00:05.5Z" precedes "12:00:05Z" as a string.
	for _, ex := range []struct {
		stamp, tag string
	}{{"2026-08-19T12:00:05Z", "a"}, {"2026-08-19T12:00:05.5Z", "b"}} {
		for part := 0; part < 8; part++ {
			var body, text string
			switch {
			case part == 0:
				text = "prompt " + ex.tag
				body = fmt.Sprintf(`{"role":"user","parts":[{"text":%q}]}`, text)
			case part%2 == 1:
				text = fmt.Sprintf("model %s-%d", ex.tag, part)
				body = fmt.Sprintf(`{"role":"model","parts":[{"text":%q}]}`, text)
			default:
				text = fmt.Sprintf("out %s-%d", ex.tag, part)
				body = fmt.Sprintf(`{"role":"user","parts":[{"functionResponse":{"name":"run_on_host","response":{"output":%q}}}]}`, text)
			}
			role := "user"
			if part%2 == 1 {
				role = "model"
			}
			name := fmt.Sprintf("%s/messages/2/%s-%d-%s.json", sessionID, ex.stamp, part, role)
			if ex.tag == "a" && part == 3 {
				// Listed but unreadable: skipped without hiding the rest.
				name = ""
			}
			if name != "" {
				objects[name] = []byte(body)
				want = append(want, text)
			} else {
				objects[fmt.Sprintf("%s/messages/2/%s-%d-%s.json", sessionID, ex.stamp, part, role)] = nil
			}
		}
	}
	var mu sync.Mutex
	inflight, maxInflight := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/b/"+bucket+"/o") {
			prefix := r.URL.Query().Get("prefix")
			var items []string
			for name := range objects {
				if strings.HasPrefix(name, prefix) {
					items = append(items, fmt.Sprintf(`{"name":%q,"bucket":%q}`, name, bucket))
				}
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
			return
		}
		body, ok := objects[strings.TrimPrefix(r.URL.Path, "/"+bucket+"/")]
		if !ok || body == nil {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		inflight++
		maxInflight = max(maxInflight, inflight)
		mu.Unlock()
		// Later parts return first, so insertion order must come from the
		// sort rather than from completion order.
		time.Sleep(time.Duration(len(body)%7) * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		w.Write(body)
	}))
	defer srv.Close()
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	deps := &Deps{GCSClient: client, SessionsBucket: bucket}
	groups := make(map[int]*IterationGroup)
	groupFor := func(n int) *IterationGroup {
		if _, ok := groups[n]; !ok {
			groups[n] = &IterationGroup{Number: n}
		}
		return groups[n]
	}
	if err := loadTranscript(context.Background(), deps, sessionID, groupFor); err != nil {
		t.Fatalf("loadTranscript: %v", err)
	}
	if len(groups) != 2 || groups[1] == nil || groups[2] == nil {
		t.Fatalf("iterations = %v; want 1 and 2", slices.Sorted(maps.Keys(groups)))
	}
	if got := groups[1].Events; len(got) != 1 || got[0].Kind != "proposal" {
		t.Errorf("iteration 1 events = %+v; want one proposal", got)
	}
	var got []string
	for _, e := range groups[2].Events {
		got = append(got, e.Body)
	}
	if !slices.Equal(got, want) {
		t.Errorf("iteration 2 bodies = %q; want %q", got, want)
	}
	if maxInflight < 2 {
		t.Errorf("max reads in flight = %d; want concurrent reads", maxInflight)
	}
}

// detached wraps cmd the way run_on_host does (internal/agent detachOutput).
func detached(cmd string) string {
	const out = "/tmp/.rb-tool-00c0ffee00c0ffee"
	return "( " + cmd + "\n) >" + out + " 2>&1; rc=$?; { rm -f " + out + "; cat; } <" + out + "; exit $rc"
}

// TestClassifyExec routes each label: a build's steps by their build, the
// tools and the session's own execs by kind, and an unknown or missing label
// as such.
func TestClassifyExec(t *testing.T) {
	const dir = "/home/builder/builds/iter-2"
	sh := func(script string) []string { return []string{"/bin/sh", "-c", script} }
	artifact := "set -eu\n[ -f \"" + dir + "/out/x.tgz\" ] || exit 44"
	// The stabilize URL's builds/ precedes the build's own paths.
	compare := strings.Join([]string{
		"set -eu",
		"fetch /home/builder/tools/stabilize https://storage.googleapis.com/prebuild/builds/v1/stabilize",
		"stab /home/builder/builds/iter-2-canon/out/x.tgz /home/builder/builds/iter-2-canon/out/x.tgz.stabilized",
		"exec /home/builder/tools/diffr --summary --label rebuild --label upstream /home/builder/builds/iter-2-canon/out/x.tgz.stabilized /home/builder/upstream/x.tgz.stabilized",
	}, "\n")
	restore := `c=$(docker ps -aq --filter name=^rb- | head -1); exec docker exec "$c" /bin/sh -c 'cd /src && git checkout --force v1 && git clean -fd'`
	check := "set -eu\ndie() { exit 3; }\nexec diffr a b"
	for _, tc := range []struct {
		name string
		exec schema.ScratchExec
		want classifiedExec
		ok   bool
	}{{
		name: "keepalive",
		exec: schema.ScratchExec{Cmd: []string{"/bin/true"}, Label: schema.ScratchLabelKeepAlive},
		want: classifiedExec{class: execKeepAlive, label: "keepalive", script: "/bin/true"},
		ok:   true,
	}, {
		name: "phase",
		exec: schema.ScratchExec{Cmd: []string{"docker", "exec", "rb-iter-2", "/bin/sh", "-c", "set -eux\nnpm ci"}, Label: schema.PhaseLabel(rebuild.PhaseDeps)},
		want: classifiedExec{class: execBuildMachinery, label: "phase/deps", buildID: "iter-2", script: "set -eux\nnpm ci"},
		ok:   true,
	}, {
		name: "build step by its script's build directory",
		exec: schema.ScratchExec{Cmd: sh(artifact), Label: schema.ScratchLabelArtifact},
		want: classifiedExec{class: execBuildMachinery, label: "artifact", buildID: "iter-2", script: artifact},
		ok:   true,
	}, {
		name: "comparison by the last build directory in its script",
		exec: schema.ScratchExec{Cmd: sh(compare), Label: schema.ScratchLabelCompare},
		want: classifiedExec{class: execBuildMachinery, label: "compare", buildID: "iter-2-canon", script: compare},
		ok:   true,
	}, {
		name: "session exec outside any build",
		exec: schema.ScratchExec{Cmd: sh(restore), Label: schema.ScratchLabelRestore},
		want: classifiedExec{class: execOther, label: "restore", script: restore},
		ok:   true,
	}, {
		name: "host tool, unwrapped from its output envelope",
		exec: schema.ScratchExec{Cmd: sh(detached("cat " + dir + "/build.sh")), Label: schema.ToolLabel("run_on_host")},
		want: classifiedExec{class: execToolHost, label: "tool/run_on_host", script: "cat " + dir + "/build.sh"},
		ok:   true,
	}, {
		name: "check_artifact",
		exec: schema.ScratchExec{Cmd: sh(check), Label: schema.ToolLabel("check_artifact")},
		want: classifiedExec{class: execToolCheck, label: "tool/check_artifact", script: check},
		ok:   true,
	}, {
		name: "unknown tool",
		exec: schema.ScratchExec{Cmd: []string{"ls"}, Label: schema.ToolLabel("run_elsewhere")},
		want: classifiedExec{class: execOther, label: "tool/run_elsewhere", script: "ls"},
	}, {
		name: "unknown label",
		exec: schema.ScratchExec{Cmd: sh("wget x.tar"), Label: "warmup"},
		want: classifiedExec{class: execOther, label: "warmup", script: "wget x.tar"},
	}, {
		name: "no label",
		exec: schema.ScratchExec{Cmd: []string{"/bin/true"}},
		want: classifiedExec{class: execOther, script: "/bin/true"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := classifyExec(tc.exec)
			if got != tc.want || ok != tc.ok {
				t.Errorf("classifyExec(%+v)\n got %+v, %v\nwant %+v, %v", tc.exec, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestComposeTrajectory(t *testing.T) {
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return start.Add(d) }
	groups := map[int]*IterationGroup{
		1: {Number: 1, Events: []TranscriptEvent{{Kind: "proposal", Title: "inference proposal"}}},
		3: {Number: 3, Events: []TranscriptEvent{
			{Kind: "prompt", Title: "prompt", At: at(10 * time.Minute)},
			{Kind: "call", Title: "run_on_host", Command: "ls builds", At: at(11 * time.Minute)},
		}},
	}
	groupFor := func(n int) *IterationGroup {
		if _, ok := groups[n]; !ok {
			groups[n] = &IterationGroup{Number: n}
		}
		return groups[n]
	}
	sh := func(script string) []string { return []string{"/bin/sh", "-c", script} }
	data := &SessionData{}
	for _, e := range []schema.ScratchExec{
		// iter-1 ran clean but its artifact differed from upstream.
		{ID: "1-prepare", Label: schema.ScratchLabelPrepare, Cmd: sh("set -eu\nmkdir -p \"/home/builder/builds/iter-1/out\""), CreatedAt: at(0)},
		{ID: "1-start", Label: schema.ScratchLabelStart, Cmd: []string{"docker", "run", "--name", "rb-iter-1", "img", "sleep", "infinity"}, CreatedAt: at(time.Second)},
		{ID: "1-phase", Label: schema.PhaseLabel(rebuild.PhaseDeps), Cmd: []string{"docker", "exec", "rb-iter-1", "/bin/sh", "-c", "set -eux\nnpm ci"}, CreatedAt: at(2 * time.Second)},
		{ID: "1-stop", Label: schema.ScratchLabelStop, Cmd: []string{"docker", "stop", "-t", "0", "rb-iter-1"}, CreatedAt: at(3 * time.Second)},
		{ID: "1-artifact", Label: schema.ScratchLabelArtifact, Cmd: sh("set -eu\n[ -f \"/home/builder/builds/iter-1/out/x.tgz\" ] || exit 44"), CreatedAt: at(4 * time.Second)},
		{ID: "1-diff", Label: schema.ScratchLabelCompare, Cmd: sh("set -eu\ndie() { exit 3; }\nexec diffr --summary /home/builder/builds/iter-1/out/x.tgz.stabilized up"), ExitCode: 1, CreatedAt: at(5 * time.Second)},
		// Predates every window: ledger only.
		{ID: "keepalive", Label: schema.ScratchLabelKeepAlive, Cmd: []string{"/bin/true"}, CreatedAt: at(6 * time.Second)},
		// The transcript shows the tool execs as calls, so none renders.
		{ID: "3-host", Label: schema.ToolLabel("run_on_host"), Cmd: sh(detached("ls builds")), CreatedAt: at(11 * time.Minute)},
		{ID: "3-container", Label: schema.ToolLabel("run_in_container"), Cmd: sh("c=$(docker ps -aq --filter name=^rb- | head -1); exec docker exec \"$c\" /bin/sh -c 'ls'"), CreatedAt: at(12 * time.Minute)},
		{ID: "3-stray", Label: schema.ToolLabel("run_on_host"), Cmd: sh(detached("cat notes")), CreatedAt: at(13 * time.Minute)},
		// A dispatch outside any build renders as an exec, as does one with
		// a label the page does not know.
		{ID: "3-restore", Label: schema.ScratchLabelRestore, Cmd: sh("exec docker exec c /bin/sh -c 'git checkout --force v1'"), CreatedAt: at(14 * time.Minute)},
		{ID: "3-warmup", Label: "warmup", Cmd: sh("wget -O - https://example/x.tar | tar -x"), CreatedAt: at(15 * time.Minute)},
		{ID: "3c-prepare", Label: schema.ScratchLabelPrepare, Cmd: sh("set -eu\nmkdir -p \"/home/builder/builds/iter-3-canon/out\""), CreatedAt: at(20 * time.Minute)},
		{ID: "3c-artifact", Label: schema.ScratchLabelArtifact, Cmd: sh("set -eu\n[ -f \"/home/builder/builds/iter-3-canon/out/x.tgz\" ] || exit 44"), CreatedAt: at(21 * time.Minute)},
		// Unlabeled: reported, and shown as is.
		{ID: "3-unlabeled", Cmd: []string{"/bin/true"}, CreatedAt: at(16 * time.Minute)},
	} {
		e.State = schema.ScratchExecCompleted
		data.Execs = append(data.Execs, newExecView("sess-1", e))
	}
	composeTrajectory(data, groups, groupFor)
	var iters []int
	for _, v := range data.Execs {
		iters = append(iters, v.Iter)
	}
	if want := []int{1, 1, 1, 1, 1, 1, 0, 3, 3, 3, 3, 3, 3, 3, 3}; !slices.Equal(iters, want) {
		t.Errorf("exec iterations = %v; want %v", iters, want)
	}
	titles := map[int][]string{}
	for n, g := range groups {
		sortEvents(g.Events)
		for _, e := range g.Events {
			titles[n] = append(titles[n], e.Title)
		}
	}
	want := map[int][]string{
		1: {"inference proposal", "build iter-1 · mismatch"},
		3: {"prompt", "run_on_host", "exec · restore · completed · exit 0 · N/A", "exec · warmup · completed · exit 0 · N/A", "exec · completed · exit 0 · N/A", "build iter-3-canon · succeeded"},
	}
	if diff := cmp.Diff(want, titles); diff != "" {
		t.Errorf("event titles (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"1 exec without a label", `unknown exec label "warmup"`}, data.Warnings); diff != "" {
		t.Errorf("warnings (-want +got):\n%s", diff)
	}
	var labels []string
	for _, s := range groups[1].Events[1].Build.Steps {
		labels = append(labels, s.Label)
	}
	if want := []string{"prepare", "start", "phase/deps", "stop", "artifact", "compare"}; !slices.Equal(labels, want) {
		t.Errorf("iter-1 steps = %v; want %v", labels, want)
	}
}

func TestBuildOutcome(t *testing.T) {
	ok := func(label string) BuildStep { return BuildStep{Label: label, State: schema.ScratchExecCompleted} }
	clean := []BuildStep{ok("prepare"), ok("start"), ok("phase/build"), ok("stop")}
	for _, tc := range []struct {
		name  string
		steps []BuildStep
		want  string
	}{
		{"failing phase", []BuildStep{ok("prepare"), ok("start"), {Label: "phase/build", State: schema.ScratchExecCompleted, ExitCode: 7}}, "phase/build exit 7"},
		{"pending", []BuildStep{ok("prepare"), {Label: "start", State: schema.ScratchExecPending}}, "running"},
		{"timed out", []BuildStep{ok("prepare"), ok("start"), {Label: "phase/build", State: schema.ScratchExecTimedOut}}, "phase/build timed_out"},
		{"no artifact step", clean, "completed"},
		{"artifact", append(slices.Clone(clean), ok("artifact")), "succeeded"},
		{"missing artifact", append(slices.Clone(clean), BuildStep{Label: "artifact", State: schema.ScratchExecCompleted, ExitCode: 44}), "artifact exit 44"},
		{"comparison found differences", append(slices.Clone(clean), ok("artifact"), BuildStep{Label: "compare", State: schema.ScratchExecCompleted, ExitCode: 1}), "mismatch"},
		{"comparison found none", append(slices.Clone(clean), ok("artifact"), ok("compare")), "mismatch"},
		{"comparison setup failed", append(slices.Clone(clean), ok("artifact"), BuildStep{Label: "compare", State: schema.ScratchExecCompleted, ExitCode: 3}), "compare exit 3"},
	} {
		if got := buildOutcome(tc.steps); got != tc.want {
			t.Errorf("%s: buildOutcome = %q; want %q", tc.name, got, tc.want)
		}
	}
}
