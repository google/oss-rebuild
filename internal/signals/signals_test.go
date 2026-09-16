// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
)

var testPrevs = []PrevalenceRecord{
	{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8},
	{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Dependents: 60, Prevalence: 0.75,
		Artifact: "pkgA-1.0.tar.gz", Published: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)},
	{Ecosystem: "pypi", Package: "pkgA", Version: "2.0", Dependents: 40, Prevalence: 0.6},
	{Ecosystem: "npm", Package: "pkgB", Dependents: 10, Prevalence: 0.4},
}

func queryText(t *testing.T, db *sqlite3.Conn, sql string) string {
	t.Helper()
	stmt, _, err := db.Prepare(sql)
	if err != nil {
		t.Fatalf("prepare %q: %v", sql, err)
	}
	defer stmt.Close()
	if !stmt.Step() {
		t.Fatalf("no rows for %q: %v", sql, stmt.Err())
	}
	return stmt.ColumnText(0)
}

// publish builds a signal database from prevs at version and publishes it
// under Object in a fresh in-memory destination.
func publish(t *testing.T, prevs []PrevalenceRecord, version int) billy.Filesystem {
	t.Helper()
	path := filepath.Join(t.TempDir(), "built.db")
	if _, err := Build(path, prevs, Meta{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := sqlitex.SetVersion(db, version); err != nil {
		t.Fatalf("SetVersion: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	dest := memfs.New()
	if err := sqlitex.Publish(dest, Object, path); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return dest
}

func TestBuildAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signals.db")
	built := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	counts, err := Build(path, testPrevs, Meta{BuiltAt: built, ToolVersion: "v-test"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if counts[TablePackageSignals] != 2 || counts[TableVersionSignals] != 2 {
		t.Errorf("counts = %v, want 2 packages and 2 versions", counts)
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	got, err := PackageSignals(db)
	if err != nil {
		t.Fatalf("PackageSignals: %v", err)
	}
	want := []PackageSignal{
		{Ecosystem: "npm", Package: "pkgB", Dependents: 10, Prevalence: 0.4, Score: 0.4},
		{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8, Score: 0.8},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("package rows mismatch (-want +got):\n%s", diff)
	}
	meta, err := ReadMeta(db)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if !meta.BuiltAt.Equal(built) || meta.ToolVersion != "v-test" {
		t.Errorf("meta = %+v, want built %v by v-test", meta, built)
	}
	// Version rows carry the registry metadata when the export provides it,
	// NULL otherwise, with times in the uniform encoding.
	if got := queryText(t, db, "SELECT published FROM version_signals WHERE version='1.0'"); got != "2026-07-15T00:00:00.000Z" {
		t.Errorf("published = %q", got)
	}
	if got := queryText(t, db, "SELECT artifact FROM version_signals WHERE version='1.0'"); got != "pkgA-1.0.tar.gz" {
		t.Errorf("artifact = %q", got)
	}
	if got := queryText(t, db, "SELECT count(*) FROM version_signals WHERE version='2.0' AND published IS NULL AND artifact IS NULL"); got != "1" {
		t.Errorf("bare version rows = %s, want 1 with NULL metadata", got)
	}
	if v, err := sqlitex.Version(db); err != nil || v != SchemaVersion {
		t.Errorf("schema version = (%d, %v), want %d", v, err, SchemaVersion)
	}
}

func TestFetchRoundTrip(t *testing.T) {
	dest := publish(t, testPrevs, SchemaVersion)
	path, err := Fetch(dest, t.TempDir())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	got, err := PackageSignals(db)
	if err != nil {
		t.Fatalf("PackageSignals: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("fetched %d package rows, want 2", len(got))
	}
}

func TestFetchRefusesOtherEra(t *testing.T) {
	dest := publish(t, nil, SchemaVersion+1)
	if _, err := Fetch(dest, t.TempDir()); err == nil {
		t.Error("Fetch accepted a database from a different schema era")
	}
}
