// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package docdb maintains indexed SQLite views of document streams.
//
// The unit of storage is a JSON document. A doc table keeps each document
// whole in its raw column and exposes it relationally. A few real columns,
// such as the primary key and the update clock, are extracted by the write
// statement itself, and every other column is GENERATED from raw, so a
// column can never disagree with the document it derives from. Writes are
// guarded upserts that keep the newer document, which makes them idempotent
// and order-independent: replaying a write, or applying old and new in
// either order, converges on the same row.
//
// A derived table is the other kind: a SELECT over the database's other
// tables, materialized once when the database is built.
//
// Changes travel between databases as segments, gzip JSONL files of bare
// documents that replay through the same guarded upserts. Cache serves a
// local copy of a published database, applying new segments as they land
// and downloading the database afresh when it is replaced wholesale.
package docdb

import (
	"encoding/json"
	"strings"

	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// TableDef describes one table. A table is either a doc table (Cols set:
// real columns extracted from each written document, an implicit raw column
// holding the document, and optionally GenCols computed from raw) or a
// derived table (Query set: a SELECT over the database's other tables,
// materialized by StoreQuery). Indexes lists the column sets to index after
// load. A real column named updated becomes the upsert guard clock, and
// without one replays are last-write-wins.
// NOTE: Names and expressions are trusted SQL fragments. They should be
// exclusively sourced from compile-time registries.
type TableDef struct {
	Name    string
	Cols    []Col
	PK      []string
	GenCols []GenCol
	Query   string
	Indexes [][]string
}

// Col is a real column of a doc table, extracted from the bound document by
// the write statement itself. Unlike generated columns, real columns can be
// primary keys and carry the upsert guard.
type Col struct {
	Name string
	Type string
	Expr string
}

// GenCol is a column generated from the stored raw document. Stored columns
// cost disk and compute at write when they are updated. Virtual ones cost
// nothing on write and compute on read, so columns in hot filters or orderings
// should be stored (or indexed, which also stores the computed values).
type GenCol struct {
	Name   string
	Type   string
	Expr   string
	Stored bool
}

// Expression helpers for Col specs (over the bound document, aka "?1").

// Doc extracts a field of the document being written.
func Doc(path string) string { return "json_extract(?1, '" + path + "')" }

// DocTime is Doc normalized to sqlitex.TimeFormat, zero time to NULL.
func DocTime(path string) string {
	return "nullif(strftime('%Y-%m-%dT%H:%M:%fZ', " + Doc(path) + "), '0001-01-01T00:00:00.000Z')"
}

// OneofKey names the single field present in a oneof object of the document
// being written (empty when none): the oneof's JSON tags double as labels.
func OneofKey(path string) string {
	return "coalesce((SELECT key FROM json_each(?1, '" + path + "') LIMIT 1), '')"
}

// Expression helpers for GenCol specs (over the stored raw column).

// Raw extracts a stored-document field.
func Raw(path string) string { return "json_extract(raw, '" + path + "')" }

// RawSeconds converts a Go duration field (integer nanoseconds) to seconds.
func RawSeconds(path string) string { return Raw(path) + " / 1e9" }

// RawTime is Raw normalized to sqlitex.TimeFormat, zero time to NULL.
func RawTime(path string) string {
	return "nullif(strftime('%Y-%m-%dT%H:%M:%fZ', " + Raw(path) + "), '0001-01-01T00:00:00.000Z')"
}

func createTableSQL(td TableDef) string {
	var parts []string
	for _, c := range td.Cols {
		parts = append(parts, c.Name+" "+c.Type)
	}
	parts = append(parts, "raw TEXT")
	for _, g := range td.GenCols {
		mode := "VIRTUAL"
		if g.Stored {
			mode = "STORED"
		}
		parts = append(parts, g.Name+" "+g.Type+" GENERATED ALWAYS AS ("+g.Expr+") "+mode)
	}
	parts = append(parts, "PRIMARY KEY ("+strings.Join(td.PK, ", ")+")")
	return "CREATE TABLE " + td.Name + " (" + strings.Join(parts, ", ") + ") WITHOUT ROWID"
}

// docUpsertSQL builds a doc table's write statement. It binds the document
// as its only parameter, and the SELECT clause extracts every real column
// from it (WHERE true disambiguates the upsert clause on an INSERT..SELECT).
// When the table carries an updated column, the conflict clause only applies
// documents at least as new as the stored one (NULL treated as oldest), so
// application is idempotent and order-independent.
func docUpsertSQL(td TableDef) string {
	names := make([]string, 0, len(td.Cols)+1)
	exprs := make([]string, 0, len(td.Cols)+1)
	pkSet := make(map[string]bool, len(td.PK))
	for _, p := range td.PK {
		pkSet[p] = true
	}
	var sets []string
	var hasUpdated bool
	for _, c := range td.Cols {
		names = append(names, c.Name)
		exprs = append(exprs, c.Expr)
		hasUpdated = hasUpdated || c.Name == "updated"
		if !pkSet[c.Name] {
			sets = append(sets, c.Name+" = excluded."+c.Name)
		}
	}
	names = append(names, "raw")
	exprs = append(exprs, "?1")
	sets = append(sets, "raw = excluded.raw")
	sql := "INSERT INTO " + td.Name + " (" + strings.Join(names, ", ") + ") SELECT " +
		strings.Join(exprs, ", ") + " WHERE true ON CONFLICT (" + strings.Join(td.PK, ", ") +
		") DO UPDATE SET " + strings.Join(sets, ", ")
	if hasUpdated {
		sql += " WHERE excluded.updated >= " + td.Name + ".updated OR " + td.Name + ".updated IS NULL"
	}
	return sql
}

func createIndexSQL(table string, idxCols []string) string {
	return "CREATE INDEX idx_" + table + "_" + strings.Join(idxCols, "_") + " ON " + table + " (" + strings.Join(idxCols, ", ") + ")"
}

// validateTable enforces the TableDef rules:
//   - a table defines one of Cols (doc table) and Query (derived table)
//   - derived tables take neither primary keys nor generated columns
//   - doc tables require a primary key over real columns (gen cols aren't keys)
//   - no column repeats or shadows the implicit "raw" column
func validateTable(td TableDef) error {
	if (len(td.Cols) > 0) == (td.Query != "") {
		return errors.Errorf("table %s: define exactly one of Cols (doc table) and Query (derived table)", td.Name)
	}
	if td.Query != "" && (len(td.PK) > 0 || len(td.GenCols) > 0) {
		return errors.Errorf("table %s: derived tables take neither primary keys nor generated columns", td.Name)
	}
	if len(td.Cols) > 0 && len(td.PK) == 0 {
		return errors.Errorf("table %s: doc tables require a primary key", td.Name)
	}
	names := map[string]bool{"raw": true}
	for _, c := range td.Cols {
		if names[c.Name] {
			return errors.Errorf("table %s: duplicate or reserved column %s", td.Name, c.Name)
		}
		names[c.Name] = true
	}
	for _, p := range td.PK {
		if !names[p] {
			return errors.Errorf("table %s: primary key column %s is not a real column", td.Name, p)
		}
	}
	for _, g := range td.GenCols {
		if names[g.Name] {
			return errors.Errorf("table %s: duplicate or reserved column %s", td.Name, g.Name)
		}
		names[g.Name] = true
	}
	return nil
}

// StoreDocs creates td's doc table, upserts the given documents, and builds
// its declared indexes.
func StoreDocs(db *sqlite3.Conn, td TableDef, docs []json.RawMessage) error {
	if err := validateTable(td); err != nil {
		return err
	}
	if len(td.Cols) == 0 {
		return errors.Errorf("table %s is not a doc table", td.Name)
	}
	if err := db.Exec(createTableSQL(td)); err != nil {
		return errors.Wrap(err, "creating table")
	}
	if err := ApplyDocs(db, td, docs); err != nil {
		return err
	}
	for _, idx := range td.Indexes {
		if err := db.Exec(createIndexSQL(td.Name, idx)); err != nil {
			return errors.Wrap(err, "creating index")
		}
	}
	return nil
}

// ApplyDocs upserts documents into td's existing doc table.
func ApplyDocs(db *sqlite3.Conn, td TableDef, docs []json.RawMessage) (err error) {
	stmt, _, err := db.Prepare(docUpsertSQL(td))
	if err != nil {
		return errors.Wrap(err, "preparing upsert")
	}
	defer stmt.Close()
	txn := db.Begin()
	defer txn.End(&err)
	for _, d := range docs {
		if err := stmt.BindText(1, string(d)); err != nil {
			return errors.Wrap(err, "binding document")
		}
		if err := stmt.Exec(); err != nil {
			return errors.Wrap(err, "applying document")
		}
	}
	return nil
}

// StoreQuery materializes a derived table from its defining query and builds
// its declared indexes, returning the row count. Materializing at build
// keeps reads cheap and snapshot-consistent. It refreshes only by rebuild.
func StoreQuery(db *sqlite3.Conn, td TableDef) (int, error) {
	if err := validateTable(td); err != nil {
		return 0, err
	}
	if td.Query == "" {
		return 0, errors.Errorf("table %s is not a derived table", td.Name)
	}
	if err := db.Exec("CREATE TABLE " + td.Name + " AS " + td.Query); err != nil {
		return 0, errors.Wrapf(err, "materializing %s", td.Name)
	}
	stmt, _, err := db.Prepare("SELECT count(*) FROM " + td.Name)
	if err != nil {
		return 0, errors.Wrap(err, "counting rows")
	}
	var n int
	if stmt.Step() {
		n = stmt.ColumnInt(0)
	}
	if err := stmt.Close(); err != nil {
		return 0, errors.Wrap(err, "counting rows")
	}
	for _, idx := range td.Indexes {
		if err := db.Exec(createIndexSQL(td.Name, idx)); err != nil {
			return 0, errors.Wrap(err, "creating index")
		}
	}
	return n, nil
}
