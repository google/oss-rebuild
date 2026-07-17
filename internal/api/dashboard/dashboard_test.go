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
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
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
	deps := &Deps{Rundex: rebuilds, Sessions: sessions}
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
	}}}
	got, err := Package(context.Background(), PackageRequest{Ecosystem: "npm", Package: "a"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].Rebuild == nil {
		t.Errorf("expected a single rebuild event, got %+v", got.Events)
	}
	var buf strings.Builder
	if err := PackageTmpl.Execute(&buf, got); err != nil {
		t.Fatalf("rendering package template: %v", err)
	}
}
