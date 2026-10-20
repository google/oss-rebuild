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
	for _, tc := range []struct{ path, contentType, want string }{
		{ThemeCSSPath, "text/css; charset=utf-8", "--accent:"},
		{CSSPath, "text/css; charset=utf-8", ".benchmark-grid"},
		{FaviconPath, "image/svg+xml", "<svg"},
		{ChartsJSPath, "text/javascript; charset=utf-8", "data-chart"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: got status %d, want %d", tc.path, rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.contentType {
			t.Errorf("GET %s: got Content-Type %q, want %q", tc.path, got, tc.contentType)
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("GET %s: body does not contain %q; wrong asset served", tc.path, tc.want)
		}
	}
}

func TestAssetsCacheAtTheirVersion(t *testing.T) {
	mux := http.NewServeMux()
	RegisterAssets(mux)
	var page strings.Builder
	if err := PackagesTmpl.Execute(&page, &PackagesData{}); err != nil {
		t.Fatal(err)
	}
	link := assetURL(CSSPath)
	if !strings.Contains(page.String(), `href="`+link+`"`) {
		t.Fatalf("page does not link %s", link)
	}
	for _, tc := range []struct{ url, cache string }{
		{link, "public, max-age=31536000, immutable"},
		{CSSPath, ""},
		{CSSPath + "?v=0", ""},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if got := rec.Header().Get("Cache-Control"); got != tc.cache {
			t.Errorf("GET %s: got Cache-Control %q, want %q", tc.url, got, tc.cache)
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
	if out := buf.String(); !strings.Contains(out, "Recent Agent Sessions") || !strings.Contains(out, `href="/session/s-new"`) {
		t.Errorf("rendered index missing session content:\n%s", out)
	}
}

func TestSessionViewOutcome(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		session               schema.AgentSession
		outcome, tone, reason string
	}{
		{"Running", schema.AgentSession{Status: schema.AgentSessionStatusRunning}, "running", "live", ""},
		{"Success", schema.AgentSession{StopReason: schema.AgentCompleteReasonSuccess, Summary: "Build successful"}, "reproduced", "ok", ""},
		{"GaveUp", schema.AgentSession{StopReason: schema.AgentCompleteReasonGaveUp, Summary: "Gave up: The data files were never committed."}, "gave up", "warn", "The data files were never committed."},
		{"Failed", schema.AgentSession{StopReason: schema.AgentCompleteReasonFailed, Summary: "Inference failed: generating strategy: no git repo"}, "failed", "bad", "no git repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := NewSessionView(tc.session)
			if got := v.Outcome(); got != tc.outcome {
				t.Errorf("Outcome() = %q, want %q", got, tc.outcome)
			}
			if got := v.Tone(); got != tc.tone {
				t.Errorf("Tone() = %q, want %q", got, tc.tone)
			}
			if got := v.Reason(); got != tc.reason {
				t.Errorf("Reason() = %q, want %q", got, tc.reason)
			}
		})
	}
}

func TestRebuildViewOutcome(t *testing.T) {
	for _, tc := range []struct {
		name          string
		attempt       schema.RebuildAttempt
		outcome, tone string
	}{
		{"Success", schema.RebuildAttempt{Status: schema.RebuildStatusSuccess, Success: true}, "reproduced", "ok"},
		{"Running", schema.RebuildAttempt{Status: schema.RebuildStatusRunning}, "running", "live"},
		{"Error", schema.RebuildAttempt{Status: schema.RebuildStatusError}, "failed", "bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := NewRebuildView(rundex.Rebuild{RebuildAttempt: tc.attempt})
			if got := v.Outcome(); got != tc.outcome {
				t.Errorf("Outcome() = %q, want %q", got, tc.outcome)
			}
			if got := v.Tone(); got != tc.tone {
				t.Errorf("Tone() = %q, want %q", got, tc.tone)
			}
		})
	}
	v := NewRebuildView(rundex.Rebuild{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1.0.0", Artifact: "a-1.0.0.tgz", RunID: "run1"}})
	if got, want := v.Href(), "/attempt/npm/a/1.0.0/a-1.0.0.tgz/run1"; got != want {
		t.Errorf("Href() = %q, want %q", got, want)
	}
}

func TestPackageHandler(t *testing.T) {
	// 1 fails by session then by rebuild, and 2 reproduces after both.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t0.Add(2 * time.Hour)
	rebuilds := &fakeReader{recent: []rundex.Rebuild{
		{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1", Artifact: "a-1.tgz", RunID: "run1", Status: schema.RebuildStatusError, Message: "getting strategy: no git repo", Created: t1}},
	}}
	sessions := &fakeSessionReader{sessions: []schema.AgentSession{
		{ID: "old-session", Target: rebuild.Target{Ecosystem: "npm", Package: "a", Version: "1"}, Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonFailed, Summary: "Maximum iterations (5) reached", Created: t0},
		{ID: "new-session", Target: rebuild.Target{Ecosystem: "npm", Package: "a", Version: "2"}, Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonSuccess, Summary: "Build successful", Created: t2},
	}}
	deps := &Deps{Rundex: rebuilds, Sessions: sessions, Registry: unreachableRegistry()}
	got, err := Package(context.Background(), PackageRequest{Ecosystem: "npm", Package: "a"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Versions) != 2 || got.Versions[0].Version != "2" || got.Versions[1].Version != "1" {
		t.Fatalf("expected versions 2 then 1, got %+v", got.Versions)
	}
	if evs := got.Versions[1].Events; len(evs) != 2 || evs[0].Href != "/session/old-session" || evs[1].Href != "/attempt/npm/a/1/a-1.tgz/run1" {
		t.Errorf("version 1 events should be the session then the rebuild: %+v", evs)
	}
	if s := got.Summary; s.LastOK == nil || s.LastOK.Href != "/session/new-session" || s.StreakRebuilds+s.StreakSessions != 0 || s.FailedRebuilds != 1 || s.FailedSessions != 1 {
		t.Errorf("unexpected summary: %+v", s)
	}
	if len(got.Causes) != 2 || got.Causes[0].Latest.Cause != "no git repo" {
		t.Errorf("expected the newer of two causes first: %+v", got.Causes)
	}
	var buf strings.Builder
	if err := PackageTmpl.Execute(&buf, got); err != nil {
		t.Fatalf("rendering package template: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`<h1>a</h1>`,
		`href="/package/npm/a/version/1"`,
		`class="mk bad agent" href="/session/old-session"`,
		`class="mk bad" href="/attempt/npm/a/1/a-1.tgz/run1"`,
		`class="mk ok agent" href="/session/new-session"`,
		`>not reproduced<`,
		`>Last reproduced<`,
		`1 failed rebuild · 1 failed agent session`,
	} {
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
	if len(got.Versions) != 1 || got.Summary.Rebuilds != 1 || got.Summary.Sessions != 0 {
		t.Errorf("expected a single rebuild, got %+v", got)
	}
	if err := PackageTmpl.Execute(new(strings.Builder), got); err != nil {
		t.Fatalf("rendering package template: %v", err)
	}
}

func TestPackageHandlerUpstream(t *testing.T) {
	// The upstream is the latest attempt's whose strategy names a repo.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	located := func(repo string) schema.StrategyOneOf {
		return schema.StrategyOneOf{LocationHint: &rebuild.LocationHint{Location: rebuild.Location{Repo: repo}}}
	}
	deps := &Deps{Rundex: &fakeReader{recent: []rundex.Rebuild{
		{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "2", RunID: "run3", Created: t0.Add(2 * time.Hour)}},
		{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1", RunID: "run1", Strategy: located("https://github.com/old/a"), Created: t0}},
		{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "2", RunID: "run2", Strategy: located("https://github.com/new/a"), Created: t0.Add(time.Hour)}},
	}}, Registry: unreachableRegistry()}
	got, err := Package(context.Background(), PackageRequest{Ecosystem: "npm", Package: "a"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.Upstream != "https://github.com/new/a" || got.UpstreamLabel() != "github.com/new/a" {
		t.Errorf("Upstream = %q (%q), want the newer repo", got.Upstream, got.UpstreamLabel())
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
	if got.Untried != 0 {
		t.Errorf("a failed lookup must not count untried versions, got %d", got.Untried)
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
