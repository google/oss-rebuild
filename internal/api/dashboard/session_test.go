// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
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
		"1K in (40% cached) / 250 out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}
