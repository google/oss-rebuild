// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package docdb

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// segmentTimeFormat is the fixed-width UTC encoding used in segment names so
// lexicographic order equals chronological order.
const segmentTimeFormat = "20060102T150405Z"

const segmentSuffix = ".jsonl.gz"

// SegmentName returns the object name for a segment written at t under a
// destination prefix. Owners version the prefix with their schema so a
// reader only ever consumes segments matching its base's era.
func SegmentName(prefix string, t time.Time) string {
	return path.Join(prefix, t.UTC().Format(segmentTimeFormat)+segmentSuffix)
}

// SegmentTime parses a segment object name back to its write time.
func SegmentTime(name string) (time.Time, error) {
	t, err := time.Parse(segmentTimeFormat, strings.TrimSuffix(path.Base(name), segmentSuffix))
	return t, errors.Wrapf(err, "parsing segment name %s", name)
}

// Delta record ops. Only upserts are emitted today. "delete" is reserved for
// tombstones should a table ever need deletion propagated faster than the
// next full rebuild.
const (
	deltaOpUpsert = "upsert"
	deltaOpDelete = "delete"
)

// deltaRecord is one line of a segment: a whole source document destined for
// one doc table, in the same encoding the table stores in raw.
type deltaRecord struct {
	Table string          `json:"table"`
	Op    string          `json:"op,omitempty"` // empty means upsert
	Doc   json.RawMessage `json:"doc"`
}

// docTableDef resolves a doc-table definition by name.
func docTableDef(defs []TableDef, name string) (TableDef, error) {
	for _, td := range defs {
		if td.Name != name {
			continue
		}
		if len(td.Cols) == 0 {
			return TableDef{}, errors.Errorf("table %s is not a doc table and cannot take deltas", name)
		}
		return td, nil
	}
	return TableDef{}, errors.Errorf("unknown table %s", name)
}

// WriteSegment writes documents (doc table name to document list) into dest
// as one gzip JSONL segment named for t under prefix. When every list is
// empty nothing is written and the returned name is empty.
func WriteSegment(dest billy.Basic, prefix string, t time.Time, defs []TableDef, tables map[string][]json.RawMessage) (_ string, err error) {
	total := 0
	for name, docs := range tables {
		if _, err := docTableDef(defs, name); err != nil {
			return "", err
		}
		total += len(docs)
	}
	if total == 0 {
		return "", nil
	}
	name := SegmentName(prefix, t)
	f, err := dest.Create(name)
	if err != nil {
		return "", errors.Wrap(err, "creating segment")
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = errors.Wrap(cerr, "closing segment")
		}
	}()
	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	for _, td := range defs { // definition order keeps segments deterministic
		for _, d := range tables[td.Name] {
			if err := enc.Encode(deltaRecord{Table: td.Name, Doc: d}); err != nil {
				return "", errors.Wrap(err, "encoding record")
			}
		}
	}
	if err := gz.Close(); err != nil {
		return "", errors.Wrap(err, "closing gzip stream")
	}
	return name, nil
}

// ListSegments returns the segment names src holds under prefix at or after
// start (a segment name, empty for all of them) in write order. A prefix
// that does not exist yet lists empty.
func ListSegments(src billy.Filesystem, prefix, start string) ([]string, error) {
	entries, err := src.ReadDir(prefix)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, errors.Wrapf(err, "listing %s", prefix)
	}
	var names []string
	for _, e := range entries {
		if name := path.Join(prefix, e.Name()); !e.IsDir() && name >= start {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// ApplySegment applies one of src's segments to db with the same guarded
// upserts the table writes use, so replaying a segment, applying one twice,
// or applying out of order can never regress a row.
func ApplySegment(db *sqlite3.Conn, src billy.Basic, defs []TableDef, name string) error {
	recs, err := fetchSegment(src, name)
	if err != nil {
		return err
	}
	return applyRecords(db, defs, recs)
}

// fetchSegment reads and parses one segment's records.
func fetchSegment(src billy.Basic, name string) ([]deltaRecord, error) {
	f, err := src.Open(name)
	if err != nil {
		return nil, errors.Wrap(err, "opening segment")
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, errors.Wrap(err, "opening gzip stream")
	}
	defer gz.Close()
	var recs []deltaRecord
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec deltaRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, errors.Wrap(err, "decoding record")
		}
		if rec.Op != "" && rec.Op != deltaOpUpsert {
			return nil, errors.Errorf("unsupported op %q in %s", rec.Op, name)
		}
		recs = append(recs, rec)
	}
	return recs, errors.Wrap(sc.Err(), "scanning segment")
}

// applyRecords applies one segment's records to db in a single transaction
// with the guarded upserts the table writes use.
func applyRecords(db *sqlite3.Conn, defs []TableDef, recs []deltaRecord) (err error) {
	stmts := map[string]*sqlite3.Stmt{}
	defer func() {
		for _, stmt := range stmts {
			stmt.Close()
		}
	}()
	txn := db.Begin()
	defer txn.End(&err)
	for _, rec := range recs {
		stmt, ok := stmts[rec.Table]
		if !ok {
			td, err := docTableDef(defs, rec.Table)
			if err != nil {
				return err
			}
			if stmt, _, err = db.Prepare(docUpsertSQL(td)); err != nil {
				return errors.Wrap(err, "preparing upsert")
			}
			stmts[rec.Table] = stmt
		}
		if err := stmt.BindText(1, string(rec.Doc)); err != nil {
			return errors.Wrap(err, "binding document")
		}
		if err := stmt.Exec(); err != nil {
			return errors.Wrap(err, "applying document")
		}
	}
	return nil
}
