// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

// Tests here cover only the methods Scratch adds on top of Resource. The
// generic CRUD (Insert/Get/Update/Upsert) is exercised by the generic
// memoryResource through any of its instantiations.

func sampleScratch(id string, state schema.ScratchState, lastUsed time.Time) schema.Scratch {
	return schema.Scratch{
		ID:           id,
		BuildID:      "build-" + id,
		MachineClass: schema.MachineClassStandard,
		VMName:       "vm-" + id,
		InternalIP:   "10.0.0.1",
		Zone:         "us-central1-a",
		State:        state,
		LastUsed:     lastUsed,
	}
}

func TestMemoryScratch_BespokeNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryScratch()
	if err := s.UpdateState(ctx, "missing", schema.ScratchReady); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateState(missing) = %v; want ErrNotFound", err)
	}
	if err := s.UpdateLastUsed(ctx, "missing", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateLastUsed(missing) = %v; want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) = %v; want ErrNotFound", err)
	}
}

func TestMemoryScratch_UpdateState(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryScratch()
	if err := s.Insert(ctx, sampleScratch("e1", schema.ScratchStarting, time.Time{})); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	before := time.Now().UTC()
	if err := s.UpdateState(ctx, "e1", schema.ScratchReady); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	got, _ := s.Get(ctx, "e1")
	if got.State != schema.ScratchReady {
		t.Errorf("State = %q; want %q", got.State, schema.ScratchReady)
	}
	if got.Updated.Before(before) {
		t.Errorf("Updated = %v; want >= %v", got.Updated, before)
	}
}

func TestMemoryScratch_UpdateLastUsed(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryScratch()
	if err := s.Insert(ctx, sampleScratch("e1", schema.ScratchReady, time.Time{})); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	before := time.Now().UTC()
	want := time.Unix(1700000000, 0).UTC()
	if err := s.UpdateLastUsed(ctx, "e1", want); err != nil {
		t.Fatalf("UpdateLastUsed: %v", err)
	}
	got, _ := s.Get(ctx, "e1")
	if !got.LastUsed.Equal(want) {
		t.Errorf("LastUsed = %v; want %v", got.LastUsed, want)
	}
	if got.Updated.Before(before) {
		t.Errorf("Updated = %v; want >= %v", got.Updated, before)
	}
}

func TestMemoryScratch_ListIdleSince(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryScratch()
	cutoff := time.Unix(1700001000, 0).UTC()
	old := cutoff.Add(-time.Hour)
	fresh := cutoff.Add(time.Hour)

	for _, e := range []schema.Scratch{
		sampleScratch("ready-old", schema.ScratchReady, old),                   // ✓ included
		sampleScratch("ready-fresh", schema.ScratchReady, fresh),               // ✗ too fresh
		sampleScratch("starting-old", schema.ScratchStarting, old),             // ✗ wrong state
		sampleScratch("deleting-old", schema.ScratchDeleting, old),             // ✗ wrong state
		sampleScratch("deleted-old", schema.ScratchDeleted, old),               // ✗ wrong state
		sampleScratch("ready-old-2", schema.ScratchReady, old.Add(-time.Hour)), // ✓ included
	} {
		if err := s.Insert(ctx, e); err != nil {
			t.Fatalf("Insert(%s): %v", e.ID, err)
		}
	}

	got, err := s.ListIdleSince(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListIdleSince: %v", err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	want := []string{"ready-old", "ready-old-2"}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("ListIdleSince ids mismatch (-want +got):\n%s", diff)
	}
}

func TestMemoryScratch_Delete(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryScratch()
	if err := s.Insert(ctx, sampleScratch("e1", schema.ScratchReady, time.Time{})); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Delete(ctx, "e1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "e1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v; want ErrNotFound", err)
	}
}
