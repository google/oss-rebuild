// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestMutate(t *testing.T) {
	ctx := context.Background()
	sessions := NewMemorySessions()
	if err := sessions.Insert(ctx, schema.AgentSession{ID: "s1", Status: schema.AgentSessionStatusRunning}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	err := sessions.Mutate(ctx, "s1", func(s *schema.AgentSession) (bool, error) {
		s.Status = schema.AgentSessionStatusCompleted
		return true, nil
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	got, err := sessions.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != schema.AgentSessionStatusCompleted {
		t.Errorf("Status = %s, want COMPLETED", got.Status)
	}
	// A skipped write leaves the stored value untouched.
	err = sessions.Mutate(ctx, "s1", func(s *schema.AgentSession) (bool, error) {
		s.Status = schema.AgentSessionStatusRunning
		return false, nil
	})
	if err != nil {
		t.Fatalf("Mutate(skip): %v", err)
	}
	got, err = sessions.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != schema.AgentSessionStatusCompleted {
		t.Errorf("skipped write changed Status to %s", got.Status)
	}
	if err := sessions.Mutate(ctx, "missing", func(*schema.AgentSession) (bool, error) { return true, nil }); err != ErrNotFound {
		t.Errorf("Mutate(missing) = %v, want ErrNotFound", err)
	}
}

func TestIterationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	iterations := NewMemoryIterations()
	i := schema.AgentIteration{ID: "i1", SessionID: "s1", Number: 1}
	if err := iterations.Insert(ctx, i); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := iterations.Get(ctx, IterationKey{SessionID: "s1", ID: "i1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "i1" || got.Number != 1 {
		t.Errorf("round trip = %+v", got)
	}
}
