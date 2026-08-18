// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestVersionHandler(t *testing.T) {
	rebuilds := &fakeReader{recent: []rundex.Rebuild{
		{RebuildAttempt: schema.RebuildAttempt{Ecosystem: "npm", Package: "a", Version: "1", Artifact: "a-1.tgz", RunID: "run1", Success: true}},
	}}
	sessions := &fakeSessionReader{sessions: []schema.AgentSession{
		{ID: "s1", Target: rebuild.Target{Ecosystem: "npm", Package: "a", Version: "1"}, Status: schema.AgentSessionStatusCompleted, StopReason: schema.AgentCompleteReasonSuccess, Summary: "fixed it"},
	}}
	deps := &Deps{Rundex: rebuilds, Sessions: sessions}

	got, err := Version(context.Background(), VersionRequest{Ecosystem: "npm", Package: "a", Version: "1"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].RunID != "run1" {
		t.Errorf("unexpected attempts: %+v", got.Attempts)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "s1" {
		t.Errorf("unexpected sessions: %+v", got.Sessions)
	}

	var buf strings.Builder
	if err := VersionTmpl.Execute(&buf, got); err != nil {
		t.Fatalf("rendering version template: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Rebuild attempts", "Agent sessions", "run1", "s1", "fixed it"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered version page missing %q", want)
		}
	}
}
