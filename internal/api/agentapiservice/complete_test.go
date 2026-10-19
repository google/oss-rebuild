// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"testing"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestAgentCompleteRecordsTheWinner(t *testing.T) {
	ctx := context.Background()
	sessions := db.NewMemorySessions()
	if err := sessions.Insert(ctx, schema.AgentSession{ID: "s", Status: schema.AgentSessionStatusRunning}); err != nil {
		t.Fatal(err)
	}
	req := schema.AgentCompleteRequest{SessionID: "s", StopReason: schema.AgentCompleteReasonSuccess, SuccessIterationID: "it", WonBy: schema.AgentWonByAgent}
	if _, err := AgentComplete(ctx, req, &AgentCompleteDeps{Sessions: sessions}); err != nil {
		t.Fatal(err)
	}
	got, err := sessions.Get(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != schema.AgentSessionStatusCompleted || got.SuccessIteration != "it" || got.WonBy != schema.AgentWonByAgent {
		t.Errorf("session = %+v, want completed with iteration it won by the agent", got)
	}
}
