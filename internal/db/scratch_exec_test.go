// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestMemoryScratchExecs_ListPending(t *testing.T) {
	ctx := context.Background()
	execs := NewMemoryScratchExecs()

	mustInsert := func(r schema.ScratchExec) {
		t.Helper()
		if err := execs.Insert(ctx, r); err != nil {
			t.Fatalf("Insert(%s): %v", r.ID, err)
		}
	}
	mustInsert(schema.ScratchExec{ID: "pending-1", ScratchID: "s-a", State: schema.ScratchExecPending})
	mustInsert(schema.ScratchExec{ID: "pending-2", ScratchID: "s-b", State: schema.ScratchExecPending})
	mustInsert(schema.ScratchExec{ID: "finalized", State: schema.ScratchExecCompleted, ExitCode: 0})

	got, err := execs.ListPending(ctx)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	want := []string{"pending-1", "pending-2"}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("pending ids (-want +got):\n%s", diff)
	}
}

func TestMemoryScratchExecs_ListByScratch(t *testing.T) {
	ctx := context.Background()
	execs := NewMemoryScratchExecs()

	mustInsert := func(r schema.ScratchExec) {
		t.Helper()
		if err := execs.Insert(ctx, r); err != nil {
			t.Fatalf("Insert(%s): %v", r.ID, err)
		}
	}
	base := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	mustInsert(schema.ScratchExec{ID: "second", ScratchID: "s-a", State: schema.ScratchExecCompleted, CreatedAt: base.Add(time.Minute)})
	mustInsert(schema.ScratchExec{ID: "first", ScratchID: "s-a", State: schema.ScratchExecCompleted, CreatedAt: base})
	mustInsert(schema.ScratchExec{ID: "other-scratch", ScratchID: "s-b", State: schema.ScratchExecPending, CreatedAt: base})

	got, err := execs.ListByScratch(ctx, "s-a")
	if err != nil {
		t.Fatalf("ListByScratch: %v", err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	want := []string{"first", "second"}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("exec ids in creation order (-want +got):\n%s", diff)
	}
}
