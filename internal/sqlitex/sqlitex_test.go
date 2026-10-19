// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sqlitex

import (
	"compress/gzip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
	"github.com/ncruces/go-sqlite3"
)

func TestTimeColumn(t *testing.T) {
	got := TimeColumn(time.Date(2026, 7, 1, 12, 0, 0, 5e6, time.FixedZone("x", 3600)))
	if got != "2026-07-01T11:00:00.005Z" {
		t.Errorf("TimeColumn = %s", got)
	}
}

func TestVersionRoundTrip(t *testing.T) {
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if v, err := Version(db); err != nil || v != 0 {
		t.Errorf("fresh Version = (%d, %v), want 0", v, err)
	}
	if err := SetVersion(db, 3); err != nil {
		t.Fatalf("SetVersion: %v", err)
	}
	if v, err := Version(db); err != nil || v != 3 {
		t.Errorf("Version = (%d, %v), want 3", v, err)
	}
	if err := CheckVersion(db, 3); err != nil {
		t.Errorf("CheckVersion(3) = %v, want nil", err)
	}
	// Exact match: both an older and a newer reader refuse.
	for _, want := range []int{2, 4} {
		if err := CheckVersion(db, want); err == nil {
			t.Errorf("CheckVersion(%d) accepted a v3 database", want)
		}
	}
}

func TestPublishFetchRoundTrip(t *testing.T) {
	built := filepath.Join(t.TempDir(), "built.db")
	db, err := sqlite3.Open(built)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec("CREATE TABLE t(x); INSERT INTO t VALUES (1), (2)"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	fs := memfs.New()
	if err := Publish(fs, "pub/probe.db.gz", built); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	stored, err := util.ReadFile(fs, "pub/probe.db.gz")
	if err != nil {
		t.Fatalf("read published: %v", err)
	}
	if len(stored) < 2 || stored[0] != 0x1f || stored[1] != 0x8b {
		t.Errorf("published object is not gzip")
	}
	fetched := filepath.Join(t.TempDir(), "fetched.db")
	if err := Fetch(fs, "pub/probe.db.gz", fetched); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	db, err = sqlite3.Open(fetched)
	if err != nil {
		t.Fatalf("open fetched: %v", err)
	}
	defer db.Close()
	stmt, _, err := db.Prepare("SELECT count(*) FROM t")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer stmt.Close()
	if !stmt.Step() || stmt.ColumnInt(0) != 2 {
		t.Errorf("fetched rows = %d, want 2", stmt.ColumnInt(0))
	}
	if err := Fetch(fs, "pub/missing.db.gz", fetched); err == nil {
		t.Error("Fetch of a missing object succeeded")
	}
}

func TestCheckPublishedVersion(t *testing.T) {
	built := filepath.Join(t.TempDir(), "built.db")
	db, err := sqlite3.Open(built)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec("CREATE TABLE t(x)"); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := SetVersion(db, 3); err != nil {
		t.Fatalf("SetVersion: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	fs := memfs.New()
	if err := Publish(fs, "probe.db.gz", built); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	text, err := archivetest.GzFile([]byte(strings.Repeat("not a database, but longer than one header", 4)), gzip.Header{})
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := util.WriteFile(fs, "text.gz", text.Bytes(), 0o644); err != nil {
		t.Fatalf("writing text.gz: %v", err)
	}
	for _, tc := range []struct {
		name string
		want int
		ok   bool
	}{
		{"probe.db.gz", 3, true},
		{"probe.db.gz", 2, false},
		{"probe.db.gz", 4, false},
		{"text.gz", 3, false},
	} {
		f, err := fs.Open(tc.name)
		if err != nil {
			t.Fatalf("open %s: %v", tc.name, err)
		}
		err = CheckPublishedVersion(f, tc.want)
		f.Close()
		if (err == nil) != tc.ok {
			t.Errorf("CheckPublishedVersion(%s, %d) = %v, want ok=%v", tc.name, tc.want, err, tc.ok)
		}
	}
}
