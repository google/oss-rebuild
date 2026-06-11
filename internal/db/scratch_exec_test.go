// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"errors"
	"sort"
	"testing"

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

// Finalize is a CAS on Pending: the first terminal write wins, later
// attempts get the stored winner back with ErrUnchanged.
func TestMemoryScratchExecs_FinalizeFirstWriterWins(t *testing.T) {
	ctx := context.Background()
	execs := NewMemoryScratchExecs()
	if err := execs.Insert(ctx, schema.ScratchExec{ID: "op", State: schema.ScratchExecPending}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	won, err := execs.Finalize(ctx, schema.ScratchExec{ID: "op", State: schema.ScratchExecCompleted, ExitCode: 3})
	if err != nil {
		t.Fatalf("Finalize(first): %v", err)
	}
	if won.State != schema.ScratchExecCompleted || won.ExitCode != 3 {
		t.Errorf("first winner = %+v; want completed/3", won)
	}

	won, err = execs.Finalize(ctx, schema.ScratchExec{ID: "op", State: schema.ScratchExecTimedOut})
	if !errors.Is(err, ErrUnchanged) {
		t.Errorf("Finalize(second) err = %v; want ErrUnchanged", err)
	}
	if won.State != schema.ScratchExecCompleted || won.ExitCode != 3 {
		t.Errorf("second returned %+v; want the stored Completed/3 record", won)
	}
	stored, _ := execs.Get(ctx, "op")
	if stored.State != schema.ScratchExecCompleted || stored.ExitCode != 3 {
		t.Errorf("stored = %+v; want completed/3 preserved", stored)
	}
}

func TestMemoryScratchExecs_FinalizeNotFound(t *testing.T) {
	_, err := NewMemoryScratchExecs().Finalize(context.Background(),
		schema.ScratchExec{ID: "missing", State: schema.ScratchExecLost})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
}
