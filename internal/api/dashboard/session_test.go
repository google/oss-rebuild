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
	if err := execs.Insert(ctx, schema.ScratchExec{
		ID:         "op-1",
		ScratchID:  "scr-1",
		Cmd:        []string{"sh", "-c", "docker build ."},
		State:      schema.ScratchExecCompleted,
		OutURI:     "gs://out-bucket/obliv/op-1/out",
		CreatedAt:  created.Add(time.Minute),
		StartedAt:  created.Add(time.Minute),
		FinishedAt: created.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
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
		"Iteration 3",
		"build ok",
		"docker build .",
		"/session/sess-1/exec/op-1/out",
		"1K in (40% cached) / 250 out",
		"exec · completed · exit 0", // trajectory event in iteration 3
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	if got.Execs[0].Iter != 3 {
		t.Errorf("exec attributed to iteration %d; want 3", got.Execs[0].Iter)
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
		name: "proposal",
		in:   "1/0-inference-proposal.json",
		want: transcriptName{Iter: 1, Proposal: true},
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
