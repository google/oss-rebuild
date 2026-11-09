// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package docdb

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
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

func TestEnsureDocTables(t *testing.T) {
	db := newDB(t)
	defs := []TableDef{probeTable, {Name: "message_counts", Query: "SELECT message, count(*) AS n FROM gen_probe GROUP BY message"}}
	must1(EnsureDocTables(db, defs))
	must1(ApplyDocs(db, probeTable, []json.RawMessage{probe("k1", "hello", dt(0))}))
	// A second pass leaves the existing table (and its rows) alone, and the
	// derived table is never created by it.
	must1(EnsureDocTables(db, defs))
	assertCount(t, db, "SELECT count(*) FROM gen_probe", "1")
	assertCount(t, db, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='message_counts'", "0")
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

// filesExceptWAL lists the cache directory without the write-ahead log
// and its index, which WAL mode keeps beside the copy.
func filesExceptWAL(c *Cache) []string {
	var names []string
	for _, e := range must(os.ReadDir(c.dir)) {
		if name := e.Name(); !strings.HasSuffix(name, "-wal") && !strings.HasSuffix(name, "-shm") {
			names = append(names, name)
		}
	}
	return names
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
	if names := filesExceptWAL(c); len(names) != 1 {
		t.Errorf("cache dir holds %v, want only the live base", names)
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

func TestCacheFreshnessDoesNotWaitOnQueries(t *testing.T) {
	ctx := context.Background()
	dest := testDest(t)
	up := Upstream{FS: dest, Object: "probe.db.gz", Deltas: "deltas", Defs: []TableDef{probeTable}, Schema: 1, Watermark: probeWatermark}
	now := dt(0)
	buildBase(t, dest, up.Object, probes(10, now), now, 1)
	c := must(OpenCache(ctx, up, time.Hour))
	defer c.Close()
	// A long query, like a page recompute after a delta, holds the copy.
	held, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	go c.Query(func(*sqlite3.Conn) error {
		close(held)
		<-release
		return nil
	})
	<-held
	got := make(chan time.Time, 1)
	go func() { got <- c.Freshness() }()
	select {
	case f := <-got:
		if !f.Equal(now) {
			t.Errorf("Freshness = %v, want %v", f, now)
		}
	case <-time.After(5 * time.Second):
		t.Error("Freshness waited on a running query")
	}
}

// probes returns n probe documents with compressible bodies, so a base's
// unpacked size dwarfs its compressed one.
func probes(n int, when time.Time) []json.RawMessage {
	docs := make([]json.RawMessage, n)
	for i := range docs {
		docs[i] = probe(fmt.Sprintf("k%d", i), strings.Repeat("x", 2000), when)
	}
	return docs
}

func TestCacheSwapHoldsOneCopy(t *testing.T) {
	ctx := context.Background()
	dest := testDest(t)
	up := Upstream{FS: dest, Object: "probe.db.gz", Deltas: "deltas", Defs: []TableDef{probeTable}, Schema: 1, Watermark: probeWatermark}
	now := dt(0)
	buildBase(t, dest, up.Object, probes(2000, now), now, 1)
	c := must(OpenCache(ctx, up, time.Hour))
	defer c.Close()
	dirBytes := func() int64 {
		var total int64
		entries, _ := os.ReadDir(c.dir)
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				total += info.Size()
			}
		}
		return total
	}
	copySize := dirBytes()
	// While the base is replaced, one goroutine queries in a loop and
	// another totals the cache directory's bytes.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var failed atomic.Int64
	var peak int64
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := c.Query(func(db *sqlite3.Conn) error { return db.Exec("SELECT count(*) FROM gen_probe") }); err != nil {
				failed.Add(1)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			peak = max(peak, dirBytes())
		}
	}()
	later := now.Add(time.Hour)
	buildBase(t, dest, up.Object, probes(2001, later), later, 1)
	must1(c.sync())
	close(stop)
	wg.Wait()
	// Queries wait out the swap rather than failing.
	if n := failed.Load(); n > 0 {
		t.Errorf("%d queries failed during the swap", n)
	}
	var got string
	must1(c.Query(func(db *sqlite3.Conn) error {
		got = queryText(t, db, "SELECT count(*) FROM gen_probe")
		return nil
	}))
	if got != "2001" {
		t.Errorf("after swap = %s, want 2001", got)
	}
	// The dir holds one copy plus the compressed download, never two copies.
	if peak > copySize*3/2 {
		t.Errorf("cache dir peaked at %d bytes, want under 1.5 copies of %d", peak, copySize)
	}
}

// failingOpen fails every Open of name, standing in for a failed download.
type failingOpen struct {
	billy.Filesystem
	name string
}

func (f failingOpen) Open(name string) (billy.File, error) {
	if name == f.name {
		return nil, errors.New("download failed")
	}
	return f.Filesystem.Open(name)
}

func TestCacheFailedRehydrate(t *testing.T) {
	ctx := context.Background()
	dest := testDest(t)
	up := Upstream{FS: dest, Object: "probe.db.gz", Deltas: "deltas", Defs: []TableDef{probeTable}, Schema: 1, Watermark: probeWatermark}
	now := dt(0)
	buildBase(t, dest, up.Object, probes(10, now), now, 1)
	c := must(OpenCache(ctx, up, time.Hour))
	defer c.Close()
	count := func() (string, error) {
		var got string
		err := c.Query(func(db *sqlite3.Conn) error {
			got = queryText(t, db, "SELECT count(*) FROM gen_probe")
			return nil
		})
		return got, err
	}
	files := func() []string {
		var names []string
		for _, e := range must(os.ReadDir(c.dir)) {
			names = append(names, e.Name())
		}
		return names
	}
	// A failed download keeps the current copy, and the next sync retries.
	buildBase(t, dest, up.Object, probes(20, now), now, 1)
	c.up.FS = failingOpen{dest, up.Object}
	if err := c.sync(); err == nil {
		t.Error("sync succeeded through a failed download")
	}
	if got, err := count(); err != nil || got != "10" {
		t.Errorf("after failed download = (%s, %v), want the current copy's 10", got, err)
	}
	c.up.FS = dest
	must1(c.sync())
	if got, err := count(); err != nil || got != "20" {
		t.Errorf("after retry = (%s, %v), want 20", got, err)
	}
	// A truncated base fails after the old copy is dropped, leaving nothing on
	// disk. Queries error until the next good base restores service.
	buildBase(t, dest, up.Object, probes(30, now), now, 1)
	full := must(util.ReadFile(dest, up.Object))
	must1(util.WriteFile(dest, up.Object, full[:len(full)/2], 0o644))
	if err := c.sync(); err == nil {
		t.Error("sync succeeded on a truncated base")
	}
	if _, err := count(); err == nil {
		t.Error("query succeeded with no local copy")
	}
	if !c.Freshness().Equal(now) {
		t.Errorf("freshness with no local copy = %v, want the dropped copy's %v", c.Freshness(), now)
	}
	if names := files(); len(names) != 0 {
		t.Errorf("cache dir after failed unpack = %v, want empty", names)
	}
	buildBase(t, dest, up.Object, probes(40, now), now, 1)
	must1(c.sync())
	if got, err := count(); err != nil || got != "40" {
		t.Errorf("after recovery = (%s, %v), want 40", got, err)
	}
	if names := filesExceptWAL(c); len(names) != 1 || names[0] != "base.db" {
		t.Errorf("cache dir after recovery = %v, want only base.db", names)
	}
}

func TestCacheSweepsOrphans(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// A finished child's pid stands in for a killed dashboard's.
	child := exec.Command("true")
	must1(child.Run())
	dead := filepath.Join(tmp, fmt.Sprintf("%s%d-1", cacheDirPrefix, child.Process.Pid))
	live := filepath.Join(tmp, fmt.Sprintf("%s%d-2", cacheDirPrefix, os.Getpid()))
	legacy := filepath.Join(tmp, cacheDirPrefix+"12345")
	for _, dir := range []string{dead, live, legacy} {
		must1(os.MkdirAll(dir, 0o700))
		must1(os.WriteFile(filepath.Join(dir, "base.db"), []byte("copy"), 0o600))
	}
	dest := testDest(t)
	up := Upstream{FS: dest, Object: "probe.db.gz", Deltas: "deltas", Defs: []TableDef{probeTable}, Schema: 1, Watermark: probeWatermark}
	buildBase(t, dest, up.Object, probes(1, dt(0)), dt(0), 1)
	c := must(OpenCache(context.Background(), up, time.Hour))
	// Only the exited owner's copy goes. A live owner's stays, and so does a
	// directory whose name carries no owner.
	for dir, want := range map[string]bool{dead: false, live: true, legacy: true, c.dir: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
	must1(c.Close())
	if _, err := os.Stat(c.dir); err == nil {
		t.Error("Close left the cache directory")
	}
}

func TestStoreDocSeqRollsBackOnFailure(t *testing.T) {
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	td := TableDef{
		Name: "things",
		Cols: []Col{{Name: "id", Type: "TEXT", Expr: Doc("$.id")}},
		PK:   []string{"id"},
	}
	boom := errors.New("source failed")
	docs := func(yield func(json.RawMessage, error) bool) {
		if !yield(json.RawMessage(`{"id":"a"}`), nil) {
			return
		}
		yield(nil, boom)
	}
	if _, err := StoreDocSeq(db, td, docs); !errors.Is(err, boom) {
		t.Fatalf("StoreDocSeq err = %v, want the source error", err)
	}
	// The table exists, since creation precedes the fill, but the row that
	// arrived before the failure was rolled back with the transaction.
	stmt, _, err := db.Prepare("SELECT count(*) FROM things")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer stmt.Close()
	if !stmt.Step() || stmt.ColumnInt(0) != 0 {
		t.Errorf("things has %d row(s) after a failed fill, want 0", stmt.ColumnInt(0))
	}
	if n, err := StoreDocSeq(db, TableDef{Name: "ok", Cols: td.Cols, PK: td.PK}, iterx.FromSlice([]json.RawMessage{json.RawMessage(`{"id":"a"}`), json.RawMessage(`{"id":"b"}`)})); err != nil || n != 2 {
		t.Errorf("StoreDocSeq = (%d, %v), want 2 rows", n, err)
	}
}

func TestCacheReplaysSegmentsInOrder(t *testing.T) {
	ctx := context.Background()
	dest := testDest(t)
	up := Upstream{FS: dest, Object: "probe.db.gz", Deltas: "deltas", Defs: []TableDef{probeTable}, Schema: 1, Watermark: probeWatermark}
	now := dt(0)
	buildBase(t, dest, up.Object, []json.RawMessage{probe("k1", "v0", now)}, now, 1)
	// Each segment rewrites k1, so its final value shows the apply order.
	const n = 3 * fetchParallelism
	for i := 1; i <= n; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		must(WriteSegment(dest, up.Deltas, at, up.Defs, map[string][]json.RawMessage{
			probeTable.Name: {probe("k1", fmt.Sprintf("v%d", i), at)},
		}))
	}
	c := must(OpenCache(ctx, up, time.Hour))
	defer c.Close()
	var got string
	must1(c.Query(func(db *sqlite3.Conn) error {
		got = queryText(t, db, "SELECT json_extract(raw, '$.Message') FROM gen_probe WHERE key = 'k1'")
		return nil
	}))
	if want := fmt.Sprintf("v%d", n); got != want {
		t.Errorf("k1 after replay = %q, want %q", got, want)
	}
	if !c.Freshness().Equal(now.Add(n * time.Minute)) {
		t.Errorf("freshness = %v, want the last segment", c.Freshness())
	}
}

func TestScopedDerive(t *testing.T) {
	dest := testDest(t)
	// key_lengths is keyed by the document key, which a rewrite cannot
	// change, so a touched document reaches exactly its own row.
	// message_counts has no scope and re-derives whole.
	scoped := TableDef{
		Name:  "key_lengths",
		Query: "SELECT key, length(message) AS n FROM gen_probe",
		Scope: &Scope{
			Keys:  []Key{{Name: "keys", Query: "SELECT key FROM touched_gen_probe"}},
			Rows:  "key IN (SELECT key FROM touched_keys)",
			Views: []View{{Table: probeTable.Name, Query: "SELECT * FROM main.gen_probe WHERE key IN (SELECT key FROM touched_keys)"}},
		},
		Indexes: [][]string{{"key"}},
	}
	whole := TableDef{Name: "message_counts", Query: "SELECT message, count(*) AS n FROM gen_probe GROUP BY 1"}
	defs := []TableDef{probeTable, scoped, whole}
	db := newDB(t)
	must1(StoreDocs(db, probeTable, []json.RawMessage{probe("k1", "hello", dt(0)), probe("k2", "hi", dt(0))}))
	must1(ApplyDocs(db, probeTable, nil))
	if counts := must(Derive(db, defs)); counts["key_lengths"] != 2 || counts["message_counts"] != 2 {
		t.Fatalf("Derive counts = %v", counts)
	}
	// A segment rewrites k1, adds k3, and replays k2 unchanged.
	name := must(WriteSegment(dest, "deltas", dt(time.Hour), defs, map[string][]json.RawMessage{
		probeTable.Name: {probe("k1", "goodbye", dt(time.Minute)), probe("k3", "hey", dt(time.Minute)), probe("k2", "hi", dt(0))},
	}))
	must1(ApplySegment(db, dest, defs, name))
	assertCount(t, db, "SELECT n FROM key_lengths WHERE key='k1'", "7")
	assertCount(t, db, "SELECT n FROM key_lengths WHERE key='k3'", "3")
	assertCount(t, db, "SELECT count(*) FROM key_lengths", "3")
	assertCount(t, db, "SELECT count(*) FROM message_counts WHERE message='goodbye' AND n=1", "1")
	assertCount(t, db, "SELECT count(*) FROM message_counts WHERE message='hello'", "0")
	// The scoped refresh equals a whole derive, and leaves nothing in the
	// temp schema behind.
	must1(db.Exec("ALTER TABLE key_lengths RENAME TO inc_key_lengths; DROP INDEX idx_key_lengths_key"))
	must(Derive(db, []TableDef{scoped}))
	assertCount(t, db, "SELECT count(*) FROM (SELECT * FROM key_lengths EXCEPT SELECT * FROM inc_key_lengths)", "0")
	assertCount(t, db, "SELECT count(*) FROM (SELECT * FROM inc_key_lengths EXCEPT SELECT * FROM key_lengths)", "0")
	assertCount(t, db, "SELECT count(*) FROM temp.sqlite_schema", "0")
	// A refresh that fails rolls the documents back with it: a scope with a
	// bad predicate, and a table built by an earlier definition, without a
	// column this one derives, so the copy stays as it was until it is
	// replaced by one built from this definition.
	bad := must(WriteSegment(dest, "deltas", dt(2*time.Hour), defs, map[string][]json.RawMessage{
		probeTable.Name: {probe("k4", "late", dt(2*time.Hour))},
	}))
	broken := scoped
	broken.Scope = &Scope{Rows: "no_such_column = 1"}
	wider := scoped
	wider.Query = "SELECT key, length(message) AS n, message FROM gen_probe"
	for name, td := range map[string]TableDef{"broken scope": broken, "older shape": wider} {
		if err := ApplySegment(db, dest, []TableDef{probeTable, td}, bad); err == nil {
			t.Fatalf("%s: expected the apply to fail", name)
		}
		assertCount(t, db, "SELECT count(*) FROM gen_probe WHERE key='k4'", "0")
		assertCount(t, db, "SELECT count(*) FROM gen_probe", "3")
		assertCount(t, db, "SELECT count(*) FROM key_lengths", "3")
		assertCount(t, db, "SELECT count(*) FROM temp.sqlite_schema", "0")
	}
	// A scope needs a derived table and a row predicate.
	for name, td := range map[string]TableDef{
		"doc-scope":  {Name: "t", Cols: []Col{{"key", "TEXT", Doc("$.Key")}}, PK: []string{"key"}, Scope: &Scope{Rows: "1"}},
		"empty-rows": {Name: "t", Query: "SELECT 1", Scope: &Scope{}},
	} {
		if err := validateTable(td); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

// TestCachePool checks what the read pool gives: a query runs through a
// sync's apply rather than waiting on it, and a rehydration waits for the
// queries in flight before it takes the copy away.
func TestCachePool(t *testing.T) {
	ctx := context.Background()
	dest := testDest(t)
	up := Upstream{FS: dest, Object: "probe.db.gz", Deltas: "deltas", Defs: []TableDef{probeTable}, Schema: 1, Watermark: probeWatermark}
	now := dt(0)
	buildBase(t, dest, up.Object, []json.RawMessage{probe("k1", "hello", now)}, now, 1)
	c := must(OpenCache(ctx, up, time.Hour))
	defer c.Close()
	// A query holds a read connection across a sync.
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	go func() {
		defer close(done)
		c.Query(func(db *sqlite3.Conn) error {
			queryText(t, db, "SELECT count(*) FROM gen_probe")
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	segTime := now.Add(5 * time.Minute)
	must(WriteSegment(dest, up.Deltas, segTime, up.Defs, map[string][]json.RawMessage{
		probeTable.Name: {probe("k2", "fresh", segTime)},
	}))
	synced := make(chan error, 1)
	go func() { synced <- c.sync() }()
	select {
	case err := <-synced:
		must1(err)
	case <-time.After(10 * time.Second):
		t.Fatal("sync waited on a query holding a read connection")
	}
	if !c.Freshness().Equal(segTime) {
		t.Errorf("freshness = %v, want %v", c.Freshness(), segTime)
	}
	// A rehydration waits for the held query, then serves the new base.
	later := now.Add(10 * time.Minute)
	buildBase(t, dest, up.Object, []json.RawMessage{probe("k3", "rolled", later)}, later, 1)
	go func() { synced <- c.sync() }()
	select {
	case err := <-synced:
		t.Fatalf("hydrate did not wait for the query in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	must1(<-synced)
	var got string
	must1(c.Query(func(db *sqlite3.Conn) error {
		got = queryText(t, db, "SELECT count(*) FROM gen_probe")
		return nil
	}))
	if got != "1" {
		t.Errorf("after rehydrate = %s rows, want the new base's 1", got)
	}
	if mode := must(pragmaText(c.writer, "journal_mode")); mode != "wal" {
		t.Errorf("journal_mode = %s, want wal", mode)
	}
}
