// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func TestLocalDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rebuild.db")
	ldb, err := OpenLocal(path)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	q := rundex.NewSingleConn(ldb.Conn)
	w, err := rundex.NewSQLiteWriter(q, Tables())
	if err != nil {
		t.Fatalf("NewSQLiteWriter: %v", err)
	}
	if err := w.WriteRun(ctx, rundex.FromRun(schema.Run{ID: "run1", BenchmarkName: "bench.json", Created: at(0)})); err != nil {
		t.Fatalf("WriteRun: %v", err)
	}
	for _, a := range []schema.RebuildAttempt{
		attempt("pypi", "absl-py", "1.0.0", "run1", true, schema.RebuildStatusSuccess, at(0)),
		attempt("pypi", "requests", "2.0.0", "run1", false, schema.RebuildStatusFailure, at(time.Minute)),
	} {
		if err := w.WriteRebuild(ctx, rundex.Rebuild{RebuildAttempt: a}); err != nil {
			t.Fatalf("WriteRebuild(%s): %v", a.Package, err)
		}
	}
	if _, err := ldb.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	r := rundex.NewSQLite(q)
	if runs, err := r.FetchRuns(ctx, rundex.FetchRunsOpts{}); err != nil || len(runs) != 1 {
		t.Errorf("FetchRuns = %v, %v, want 1 run", runs, err)
	}
	rebuilds, err := r.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{})
	if err != nil || len(rebuilds) != 2 {
		t.Fatalf("FetchRebuilds = %d rebuilds, %v, want 2", len(rebuilds), err)
	}
	if err := ldb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Reopening finds the same era. A rewrite of an attempt supersedes it.
	ldb, err = OpenLocal(path)
	if err != nil {
		t.Fatalf("OpenLocal(existing): %v", err)
	}
	defer ldb.Close()
	q = rundex.NewSingleConn(ldb.Conn)
	w, err = rundex.NewSQLiteWriter(q, Tables())
	if err != nil {
		t.Fatalf("NewSQLiteWriter: %v", err)
	}
	retry := attempt("pypi", "requests", "2.0.0", "run1", true, schema.RebuildStatusSuccess, at(2*time.Minute))
	if err := w.WriteRebuild(ctx, rundex.Rebuild{RebuildAttempt: retry}); err != nil {
		t.Fatalf("WriteRebuild(retry): %v", err)
	}
	if _, err := ldb.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	assertCount(t, ldb.Conn, "SELECT count(*) FROM attempts", "2")
	assertCount(t, ldb.Conn, "SELECT count(*) FROM attempts WHERE success", "2")
}

func TestOpenLocalRefusesOtherEra(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rebuild.db")
	ldb, err := OpenLocal(path)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	if err := sqlitex.SetVersion(ldb.Conn, SchemaVersion+1); err != nil {
		t.Fatalf("bumping stored version: %v", err)
	}
	if err := ldb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := OpenLocal(path); err == nil {
		t.Error("OpenLocal accepted a database from another schema era")
	}
}
