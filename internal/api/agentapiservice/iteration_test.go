// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package agentapiservice

import (
	"context"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/db"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReserveIteration(t *testing.T) {
	ctx := context.Background()
	sessions := db.NewMemorySessions()
	now := time.Now().UTC()
	for _, s := range []schema.AgentSession{
		{ID: "running", Status: schema.AgentSessionStatusRunning, MaxIterations: 2},
		{ID: "done", Status: schema.AgentSessionStatusCompleted, MaxIterations: 2},
	} {
		if err := sessions.Insert(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
	}
	// Numbers are claimed in sequence up to the limit.
	for want := 1; want <= 2; want++ {
		s, err := reserveIteration(ctx, sessions, "running", now)
		if err != nil {
			t.Fatalf("reserve %d: %v", want, err)
		}
		if s.IterationCount != want {
			t.Errorf("reserve %d: IterationCount = %d", want, s.IterationCount)
		}
	}
	stored, err := sessions.Get(ctx, "running")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.IterationCount != 2 || !stored.Updated.Equal(now) {
		t.Errorf("stored session = %+v", stored)
	}
	for _, tc := range []struct {
		name, session string
		code          codes.Code
	}{
		{"limit reached", "running", codes.FailedPrecondition},
		{"not running", "done", codes.FailedPrecondition},
		{"missing", "missing", codes.NotFound},
	} {
		_, err := reserveIteration(ctx, sessions, tc.session, now)
		if status.Code(err) != tc.code {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.code)
		}
	}
	// A refused reservation leaves the count untouched.
	if stored, _ = sessions.Get(ctx, "running"); stored.IterationCount != 2 {
		t.Errorf("refused reservation changed IterationCount to %d", stored.IterationCount)
	}
}
