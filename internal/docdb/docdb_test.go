// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package docdb

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
)

// must and must1 crash the test on plumbing errors, reserving explicit
// checks for the ones carrying want/got semantics.
func must[T any](v T, err error) T {
	must1(err)
	return v
}

func must1(err error) {
	if err != nil {
		panic(err)
	}
}

// probeDoc is a stand-in source document.
type probeDoc struct {
	Key     string
	Message string
	Nanos   int64
	When    time.Time
}

// probeTable is a doc table: the write statement extracts key and updated
// from each document, and the generated columns derive from the stored raw.
var probeTable = TableDef{
	Name: "gen_probe",
	Cols: []Col{
		{"key", "TEXT", Doc("$.Key")},
		{"updated", "TEXT", DocTime("$.When")},
	},
	PK: []string{"key"},
	GenCols: []GenCol{
		{"message", "TEXT", Raw("$.Message"), true},
		{"seconds", "REAL", RawSeconds("$.Nanos"), false},
		{"when_at", "TEXT", RawTime("$.When"), false},
	},
	Indexes: [][]string{{"message"}},
}

func dt(d time.Duration) time.Time {
	return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).Add(d)
}

func probe(key, message string, when time.Time) json.RawMessage {
	return must(json.Marshal(probeDoc{Key: key, Message: message, Nanos: 120e9, When: when}))
}

func newDB(t *testing.T) *sqlite3.Conn {
	t.Helper()
	db := must(sqlite3.Open(":memory:"))
	t.Cleanup(func() { db.Close() })
	return db
}

// testDest opens a fresh local filesystem to stand in for a destination.
// Cache fingerprints the base by mtime, which memfs reports as the current
// time on every call, so tests use real files.
func testDest(t *testing.T) billy.Filesystem {
	t.Helper()
	return osfs.New(t.TempDir())
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

func assertCount(t *testing.T, db *sqlite3.Conn, sql string, want string) {
	t.Helper()
	if got := queryText(t, db, sql); got != want {
		t.Errorf("%s = %s, want %s", sql, got, want)
	}
}

func TestDocTable(t *testing.T) {
	db := newDB(t)
	base := dt(0)
	must1(StoreDocs(db, probeTable, []json.RawMessage{probe("k1", "hello", base)}))
	// Real and generated columns extract from the document, transforms
	// applied, all times in the uniform encoding.
	assertCount(t, db, "SELECT count(*) FROM gen_probe WHERE key='k1' AND message='hello' AND seconds=120.0", "1")
	for _, col := range []string{"updated", "when_at"} {
		if got := queryText(t, db, "SELECT "+col+" FROM gen_probe"); got != "2026-07-01T12:00:00.000Z" {
			t.Errorf("%s = %s", col, got)
		}
	}
	// The guarded upsert reduces to re-applying a newer document. Generated
	// columns recompute from the new raw, and a stale replay is a no-op.
	must1(ApplyDocs(db, probeTable, []json.RawMessage{probe("k1", "newer", base.Add(time.Minute))}))
	assertCount(t, db, "SELECT count(*) FROM gen_probe WHERE message='newer'", "1")
	must1(ApplyDocs(db, probeTable, []json.RawMessage{probe("k1", "stale", base.Add(-time.Minute))}))
	assertCount(t, db, "SELECT count(*) FROM gen_probe WHERE message='newer'", "1")
	assertCount(t, db, "SELECT count(*) FROM gen_probe", "1")
	// The engine rejects direct writes to generated columns, which is what
	// makes drift impossible.
	if err := db.Exec("UPDATE gen_probe SET message='nope'"); err == nil {
		t.Error("expected error updating a generated column")
	}
	// A document without key fields extracts NULL keys, which the WITHOUT
	// ROWID primary key rejects loudly.
	if err := ApplyDocs(db, probeTable, []json.RawMessage{json.RawMessage(`{"unrelated": true}`)}); err == nil {
		t.Error("expected NOT NULL rejection for a document without key fields")
	}
	// Zero times read NULL through the normalizing expressions.
	must1(ApplyDocs(db, probeTable, []json.RawMessage{probe("k2", "m", time.Time{})}))
	assertCount(t, db, "SELECT count(*) FROM gen_probe WHERE key='k2' AND updated IS NULL AND when_at IS NULL", "1")
}

func TestQueryTable(t *testing.T) {
	db := newDB(t)
	must1(StoreDocs(db, probeTable, []json.RawMessage{
		probe("k1", "hello", dt(0)), probe("k2", "hello", dt(time.Minute)), probe("k3", "bye", dt(0)),
	}))
	td := TableDef{
		Name:    "message_counts",
		Query:   "SELECT message, count(*) AS n FROM gen_probe GROUP BY 1 ORDER BY 1",
		Indexes: [][]string{{"message"}},
	}
	if n := must(StoreQuery(db, td)); n != 2 {
		t.Errorf("materialized rows = %d, want 2", n)
	}
	assertCount(t, db, "SELECT n FROM message_counts WHERE message='hello'", "2")
	if _, err := StoreQuery(db, probeTable); err == nil {
		t.Error("expected error materializing a doc table")
	}
}

func TestValidateTable(t *testing.T) {
	docCols := []Col{{"key", "TEXT", Doc("$.Key")}}
	for name, td := range map[string]TableDef{
		"both-shapes":     {Name: "t", Cols: docCols, Query: "SELECT 1", PK: []string{"key"}},
		"neither-shape":   {Name: "t"},
		"collision":       {Name: "t", Cols: docCols, PK: []string{"key"}, GenCols: []GenCol{{"raw", "TEXT", Raw("$.X"), false}}},
		"derived-pk":      {Name: "t", Query: "SELECT 1", PK: []string{"key"}},
		"derived-gencols": {Name: "t", Query: "SELECT 1", GenCols: []GenCol{{"x", "TEXT", Raw("$.X"), false}}},
		"generated-pk":    {Name: "t", Cols: docCols, PK: []string{"missing"}},
		"doc-no-pk":       {Name: "t", Cols: docCols},
		"col-named-raw":   {Name: "t", Cols: []Col{{"raw", "TEXT", Doc("$.X")}}, PK: []string{"raw"}},
		"dup-col":         {Name: "t", Cols: []Col{{"key", "TEXT", Doc("$.Key")}, {"key", "TEXT", Doc("$.K2")}}, PK: []string{"key"}},
		"dup-gencol":      {Name: "t", Cols: docCols, PK: []string{"key"}, GenCols: []GenCol{{"x", "TEXT", Raw("$.X"), false}, {"x", "TEXT", Raw("$.Y"), false}}},
	} {
		if err := validateTable(td); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestSegmentNameRoundTrip(t *testing.T) {
	ts := time.Date(2026, 8, 20, 14, 5, 0, 0, time.UTC)
	name := SegmentName("deltas", ts)
	if name != "deltas/20260820T140500Z.jsonl.gz" {
		t.Errorf("SegmentName = %q", name)
	}
	if got := must(SegmentTime(name)); !got.Equal(ts) {
		t.Errorf("SegmentTime = %v, want %v", got, ts)
	}
}

func TestWriteAndApplySegment(t *testing.T) {
	dest := testDest(t)
	defs := []TableDef{probeTable}
	db := newDB(t)
	must1(StoreDocs(db, probeTable, []json.RawMessage{probe("k1", "hello", dt(time.Minute))}))
	// An unwritten prefix lists empty rather than erroring.
	if segs := must(ListSegments(dest, "deltas", "")); len(segs) != 0 {
		t.Fatalf("ListSegments of a missing prefix = %v, want none", segs)
	}
	name := must(WriteSegment(dest, "deltas", dt(10*time.Minute), defs, map[string][]json.RawMessage{
		probeTable.Name: {probe("k1", "newer", dt(5*time.Minute)), probe("k2", "fresh", dt(6*time.Minute))},
	}))
	if segs := must(ListSegments(dest, "deltas", "")); len(segs) != 1 || segs[0] != name {
		t.Fatalf("ListSegments = %v, want [%s]", segs, name)
	}
	must1(ApplySegment(db, dest, defs, name))
	assertCount(t, db, "SELECT count(*) FROM gen_probe WHERE message IN ('newer','fresh')", "2")
	// Replay is a no-op. A stale segment cannot regress a row.
	must1(ApplySegment(db, dest, defs, name))
	assertCount(t, db, "SELECT count(*) FROM gen_probe", "2")
	stale := must(WriteSegment(dest, "deltas", dt(11*time.Minute), defs, map[string][]json.RawMessage{
		probeTable.Name: {probe("k1", "old", dt(2*time.Minute))},
	}))
	must1(ApplySegment(db, dest, defs, stale))
	assertCount(t, db, "SELECT count(*) FROM gen_probe WHERE message='newer'", "1")
	// A start name bounds the listing inclusively.
	if segs := must(ListSegments(dest, "deltas", stale)); len(segs) != 1 || segs[0] != stale {
		t.Errorf("ListSegments from %s = %v, want only it", stale, segs)
	}
	// Empty writes produce no segment. Non-doc and unknown tables reject.
	if name, err := WriteSegment(dest, "deltas", dt(12*time.Minute), defs, map[string][]json.RawMessage{probeTable.Name: nil}); err != nil || name != "" {
		t.Errorf("empty WriteSegment = (%q, %v), want no segment", name, err)
	}
	if _, err := WriteSegment(dest, "deltas", dt(0), defs, map[string][]json.RawMessage{"nope": {probe("k", "m", dt(0))}}); err == nil {
		t.Error("expected error for unknown table")
	}
	if _, err := WriteSegment(dest, "deltas", dt(0), []TableDef{{Name: "rollup", Query: "SELECT 1"}}, map[string][]json.RawMessage{"rollup": {probe("k", "m", dt(0))}}); err == nil {
		t.Error("expected error for a derived table")
	}
}

func TestApplySegmentRejectsUnknownOp(t *testing.T) {
	dest := testDest(t)
	name := SegmentName("deltas", dt(0))
	f := must(dest.Create(name))
	gz := gzip.NewWriter(f)
	must1(json.NewEncoder(gz).Encode(deltaRecord{Table: probeTable.Name, Op: deltaOpDelete, Doc: probe("k1", "m", dt(0))}))
	must1(gz.Close())
	must1(f.Close())
	db := newDB(t)
	must1(StoreDocs(db, probeTable, nil))
	if err := ApplySegment(db, dest, []TableDef{probeTable}, name); err == nil {
		t.Error("expected error for delete op")
	}
}

// probeWatermark reads the watermark a probe base records in its own
// provenance table, standing in for an artifact's meta reader.
func probeWatermark(db *sqlite3.Conn) (time.Time, error) {
	stmt, _, err := db.Prepare("SELECT watermark FROM probe_meta")
	if err != nil {
		return time.Time{}, err
	}
	defer stmt.Close()
	if !stmt.Step() {
		return time.Time{}, stmt.Err()
	}
	return time.Parse(sqlitex.TimeFormat, stmt.ColumnText(0))
}

// buildBase publishes a probe database with the given documents, watermark,
// and schema era to dest under object.
func buildBase(t *testing.T, dest billy.Basic, object string, docs []json.RawMessage, watermark time.Time, version int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "base.db")
	db := must(sqlite3.Open(path))
	must1(StoreDocs(db, probeTable, docs))
	must1(db.Exec("CREATE TABLE probe_meta (watermark TEXT); INSERT INTO probe_meta VALUES ('" + sqlitex.TimeColumn(watermark) + "')"))
	must1(sqlitex.SetVersion(db, version))
	must1(db.Close())
	must1(sqlitex.Publish(dest, object, path))
}

func TestCache(t *testing.T) {
	ctx := context.Background()
	dest := testDest(t)
	up := Upstream{FS: dest, Object: "probe.db.gz", Deltas: "deltas", Defs: []TableDef{probeTable}, Schema: 1, Watermark: probeWatermark}
	now := dt(0)
	buildBase(t, dest, up.Object, []json.RawMessage{probe("k1", "hello", now)}, now, 1)
	// A long interval keeps the background loop idle. Refreshes run manually.
	c := must(OpenCache(ctx, up, time.Hour))
	defer c.Close()
	count := func(sql string) string {
		t.Helper()
		var got string
		must1(c.Query(func(db *sqlite3.Conn) error {
			got = queryText(t, db, sql)
			return nil
		}))
		return got
	}
	if got := count("SELECT count(*) FROM gen_probe"); got != "1" {
		t.Errorf("base rows = %s, want 1", got)
	}
	if !c.Freshness().Equal(now) {
		t.Errorf("base freshness = %v, want %v", c.Freshness(), now)
	}
	// A new delta segment applies in place and advances freshness.
	segTime := now.Add(5 * time.Minute)
	must(WriteSegment(dest, up.Deltas, segTime, up.Defs, map[string][]json.RawMessage{
		probeTable.Name: {probe("k2", "fresh", segTime)},
	}))
	must1(c.sync())
	if got := count("SELECT count(*) FROM gen_probe"); got != "2" {
		t.Errorf("after delta = %s, want 2", got)
	}
	if !c.Freshness().Equal(segTime) {
		t.Errorf("delta freshness = %v, want %v", c.Freshness(), segTime)
	}
	// A replaced base triggers a full rehydrate. Older segments are covered
	// by the new watermark and skipped.
	later := now.Add(10 * time.Minute)
	buildBase(t, dest, up.Object, []json.RawMessage{
		probe("k1", "hello", now), probe("k2", "fresh", segTime), probe("k3", "rolled", later),
	}, later, 1)
	must1(c.sync())
	if got := count("SELECT count(*) FROM gen_probe"); got != "3" {
		t.Errorf("after rehydrate = %s, want 3", got)
	}
	// A base replaced with a newer schema is refused: the cache keeps
	// serving its current copy, removes the failed download, and skips the
	// refused object on later ticks instead of re-downloading it.
	buildBase(t, dest, up.Object, nil, later, 2)
	if err := c.sync(); err == nil {
		t.Error("expected refusal of a newer schema version in place")
	}
	if err := c.sync(); err != nil {
		t.Errorf("refused base should be skipped, got %v", err)
	}
	if got := count("SELECT count(*) FROM gen_probe"); got != "3" {
		t.Errorf("after refusal = %s, want the previous copy's 3", got)
	}
	if entries := must(os.ReadDir(c.dir)); len(entries) != 1 {
		t.Errorf("cache dir holds %d files, want only the live base", len(entries))
	}
	// Any other era is refused at open, too: newer, and older or unstamped.
	for _, era := range []int{2, 0} {
		other := up
		other.Object = fmt.Sprintf("era%d.db.gz", era)
		buildBase(t, dest, other.Object, nil, later, era)
		if _, err := OpenCache(ctx, other, time.Hour); err == nil {
			t.Errorf("OpenCache accepted a v%d base for a v%d reader", era, up.Schema)
		}
	}
}
