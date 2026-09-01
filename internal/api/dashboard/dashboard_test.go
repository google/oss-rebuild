// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/meta"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/pkg/errors"
)

func TestRegisterAssets(t *testing.T) {
	mux := http.NewServeMux()
	RegisterAssets(mux)
	for path, want := range map[string]string{ThemeCSSPath: "--accent:", CSSPath: ".benchmark-grid"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: got status %d, want %d", path, rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/css" {
			t.Errorf("GET %s: got Content-Type %q, want text/css", path, got)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s: body does not contain %q; wrong asset served", path, want)
		}
	}
}

func TestApplySuccessRegex(t *testing.T) {
	tests := []struct {
		name         string
		successRegex *regexp.Regexp
		rebuilds     []rundex.Rebuild
		wantSuccess  []bool
	}{
		{
			name:         "no regex",
			successRegex: nil,
			rebuilds: []rundex.Rebuild{
				{RebuildAttempt: schema.RebuildAttempt{Success: false, Message: "failed"}},
			},
			wantSuccess: []bool{false},
		},
		{
			name:         "matching regex",
			successRegex: regexp.MustCompile("expected failure"),
			rebuilds: []rundex.Rebuild{
				{RebuildAttempt: schema.RebuildAttempt{Success: false, Message: "this is an expected failure"}},
			},
			wantSuccess: []bool{true},
		},
		{
			name:         "non-matching regex",
			successRegex: regexp.MustCompile("expected failure"),
			rebuilds: []rundex.Rebuild{
				{RebuildAttempt: schema.RebuildAttempt{Success: false, Message: "actual failure"}},
			},
			wantSuccess: []bool{false},
		},
		{
			name:         "already successful",
			successRegex: regexp.MustCompile(".*"),
			rebuilds: []rundex.Rebuild{
				{RebuildAttempt: schema.RebuildAttempt{Success: true, Message: ""}},
			},
			wantSuccess: []bool{true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := &Deps{SuccessRegex: tt.successRegex}
			applySuccessRegex(deps.SuccessRegex, tt.rebuilds)
			for i, rb := range tt.rebuilds {
				if rb.Success != tt.wantSuccess[i] {
					t.Errorf("rebuild %d success = %v, want %v", i, rb.Success, tt.wantSuccess[i])
				}
			}
		})
	}
}

// fakeReader is a rundex.Reader that only implements the methods the package handler uses.
type fakeReader struct {
	rundex.Reader
	recent []rundex.Rebuild
}

func (f *fakeReader) RecentRebuilds(context.Context) ([]rundex.Rebuild, error) { return f.recent, nil }
func (f *fakeReader) RecentPackageRebuilds(context.Context, rebuild.Ecosystem, string) ([]rundex.Rebuild, error) {
	return f.recent, nil
}
func (f *fakeReader) FetchRebuilds(context.Context, *rundex.FetchRebuildRequest) ([]rundex.Rebuild, error) {
	return f.recent, nil
}

// failingClient stands in for the registry in tests that don't exercise version
// enumeration. Its failures drive Package's degraded path.
type failingClient struct{}

func (failingClient) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("no registry in tests")
}

func unreachableRegistry() rebuild.RegistryMux { return meta.NewRegistryMux(failingClient{}) }

// fakeSessionReader is a rundex.SessionReader returning canned sessions.
type fakeSessionReader struct {
	sessions []schema.AgentSession
}

func (f *fakeSessionReader) FetchSessions(context.Context, *rundex.FetchSessionsReq) ([]schema.AgentSession, error) {
	return f.sessions, nil
}
func (f *fakeSessionReader) FetchIterations(context.Context, *rundex.FetchIterationsReq) ([]schema.AgentIteration, error) {
	return nil, nil
}

func TestIndexHandlerSessions(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	sessions := &fakeSessionReader{sessions: []schema.AgentSession{
		{ID: "s-old", Target: rebuild.Target{Ecosystem: "npm", Package: "a", Version: "1"}, Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonFailed, Created: older},
		{ID: "s-new", Target: rebuild.Target{Ecosystem: "npm", Package: "b", Version: "2"}, Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonSuccess, Summary: "Build successful", Created: newer},
	}}
	recent := []rundex.Rebuild{
		{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1"}},
	}
	deps := &Deps{Rundex: &fakeReader{recent: recent}, Sessions: sessions}
	got, err := Index(context.Background(), IndexRequest{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	// Sessions present, most-recent-first, with encoded target for the package link.
	if len(got.RecentSessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got.RecentSessions))
	}
	if got.RecentSessions[0].ID != "s-new" || got.RecentSessions[1].ID != "s-old" {
		t.Errorf("sessions not sorted most-recent-first: %+v", got.RecentSessions)
	}
	if got.RecentSessions[0].Encoded.Package == "" {
		t.Error("expected encoded target on session view")
	}
	var buf strings.Builder
	if err := IndexTmpl.Execute(&buf, got); err != nil {
		t.Fatalf("rendering index template: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "Recent Agent Sessions") || !strings.Contains(out, "Build successful") {
		t.Errorf("rendered index missing session content:\n%s", out)
	}
}

func TestPackageHandler(t *testing.T) {
	// Timeline: session@t0, rebuild@t1, session@t2 — expect most-recent-first
	// with attempts and sessions intermingled.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t0.Add(2 * time.Hour)
	rebuilds := &fakeReader{recent: []rundex.Rebuild{
		{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1", RunID: "run1", Created: t1}},
	}}
	sessions := &fakeSessionReader{sessions: []schema.AgentSession{
		{ID: "old-session", Target: rebuild.Target{Ecosystem: "npm", Package: "a", Version: "1"}, Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonFailed, Created: t0},
		{ID: "new-session", Target: rebuild.Target{Ecosystem: "npm", Package: "a", Version: "2"}, Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonSuccess, Summary: "Build successful", Created: t2},
	}}
	deps := &Deps{Rundex: rebuilds, Sessions: sessions, Registry: unreachableRegistry()}
	got, err := Package(context.Background(), PackageRequest{Ecosystem: "npm", Package: "a"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got.Events))
	}
	// Most recent first: new-session (t2), rebuild (t1), old-session (t0).
	if got.Events[0].Session == nil || got.Events[0].Session.ID != "new-session" {
		t.Errorf("event[0] should be new-session: %+v", got.Events[0])
	}
	if got.Events[1].Rebuild == nil || got.Events[1].Rebuild.RunID != "run1" {
		t.Errorf("event[1] should be rebuild run1: %+v", got.Events[1])
	}
	if got.Events[2].Session == nil || got.Events[2].Session.ID != "old-session" {
		t.Errorf("event[2] should be old-session: %+v", got.Events[2])
	}
	// The template must render the merged timeline without error.
	var buf strings.Builder
	if err := PackageTmpl.Execute(&buf, got); err != nil {
		t.Fatalf("rendering package template: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"<td>Agent</td>", "<td>Rebuild</td>", "Build successful", "new-session"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q:\n%s", want, out)
		}
	}
}

func TestPackageHandlerNilSessions(t *testing.T) {
	// A nil session reader should still render the page with only rebuilds.
	deps := &Deps{Rundex: &fakeReader{recent: []rundex.Rebuild{
		{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1", RunID: "run1"}},
	}}, Registry: unreachableRegistry()}
	got, err := Package(context.Background(), PackageRequest{Ecosystem: "npm", Package: "a"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].Rebuild == nil {
		t.Errorf("expected a single rebuild event, got %+v", got.Events)
	}
	if err := PackageTmpl.Execute(new(strings.Builder), got); err != nil {
		t.Fatalf("rendering package template: %v", err)
	}
}

func TestPackageHandlerFilteredTimelines(t *testing.T) {
	// The per-kind tabs are the merged timeline filtered, so each must carry only
	// its own kind and together they must account for every event.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	deps := &Deps{
		Rundex: &fakeReader{recent: []rundex.Rebuild{
			{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1", RunID: "run1", Created: t0.Add(time.Hour)}},
		}},
		Sessions: &fakeSessionReader{sessions: []schema.AgentSession{
			{ID: "s1", Target: rebuild.Target{Ecosystem: "npm", Package: "a", Version: "1"}, Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonSuccess, Created: t0},
		}},
		Registry: unreachableRegistry(),
	}
	got, err := Package(context.Background(), PackageRequest{Ecosystem: "npm", Package: "a"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RebuildEvents) != 1 || got.RebuildEvents[0].Rebuild == nil || got.RebuildEvents[0].Session != nil {
		t.Errorf("RebuildEvents should hold only rebuilds: %+v", got.RebuildEvents)
	}
	if len(got.SessionEvents) != 1 || got.SessionEvents[0].Session == nil || got.SessionEvents[0].Rebuild != nil {
		t.Errorf("SessionEvents should hold only sessions: %+v", got.SessionEvents)
	}
	if len(got.Events) != len(got.RebuildEvents)+len(got.SessionEvents) {
		t.Errorf("merged timeline missed events: %d, want %d", len(got.Events), len(got.RebuildEvents)+len(got.SessionEvents))
	}
	var buf strings.Builder
	if err := PackageTmpl.Execute(&buf, got); err != nil {
		t.Fatalf("rendering package template: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"panel-rebuilds", "panel-sessions", ">Rebuilds<", ">Agent sessions<"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q", want)
		}
	}
}

func TestPackageHandlerVersionListingFailure(t *testing.T) {
	// A failed lookup must be distinguishable from an ecosystem that has no
	// lister, since only one of the two is worth telling the reader about.
	deps := &Deps{
		Rundex: &fakeReader{recent: []rundex.Rebuild{
			{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1", RunID: "run1"}},
		}},
		Registry: unreachableRegistry(),
	}
	got, err := Package(context.Background(), PackageRequest{Ecosystem: "npm", Package: "a"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Warning == "" {
		t.Error("expected a warning for a failed version lookup")
	}
	if got.Summary.Supported {
		t.Error("a failed lookup must not report the version history as enumerable")
	}

	// An ecosystem with no lister degrades identically but stays quiet.
	got, err = Package(context.Background(), PackageRequest{Ecosystem: "debian", Package: "curl"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Warning != "" {
		t.Errorf("no-lister case should not warn, got %q", got.Warning)
	}
}
