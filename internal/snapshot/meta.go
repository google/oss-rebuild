// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"time"

	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// Meta is the single-row provenance record of a snapshot database, stored
// in its snapshot_meta table. The schema era lives in the file header
// instead (sqlitex.Version).
type Meta struct {
	BuiltAt       time.Time // when the build began
	Watermark     time.Time // every source write before this is captured, so replay resumes here
	SourceProject string    // the scanned source project
	ToolVersion   string    // builder binary version
}

// WriteMeta replaces the database's meta row. Zero times store NULL so they
// round-trip as zero.
func WriteMeta(db *sqlite3.Conn, m Meta) error {
	if err := db.Exec("CREATE TABLE IF NOT EXISTS snapshot_meta (built_at TEXT, watermark TEXT, source_project TEXT, tool_version TEXT)"); err != nil {
		return errors.Wrap(err, "creating snapshot_meta")
	}
	if err := db.Exec("DELETE FROM snapshot_meta"); err != nil {
		return errors.Wrap(err, "clearing snapshot_meta")
	}
	stmt, _, err := db.Prepare("INSERT INTO snapshot_meta (built_at, watermark, source_project, tool_version) VALUES (?, ?, ?, ?)")
	if err != nil {
		return errors.Wrap(err, "preparing snapshot_meta insert")
	}
	defer stmt.Close()
	for i, t := range []time.Time{m.BuiltAt, m.Watermark} {
		if t.IsZero() {
			if err := stmt.BindNull(i + 1); err != nil {
				return err
			}
		} else if err := stmt.BindText(i+1, sqlitex.TimeColumn(t)); err != nil {
			return err
		}
	}
	if err := stmt.BindText(3, m.SourceProject); err != nil {
		return err
	}
	if err := stmt.BindText(4, m.ToolVersion); err != nil {
		return err
	}
	return errors.Wrap(stmt.Exec(), "writing snapshot_meta")
}

// ReadMeta loads the database's meta row, erroring when none was written.
func ReadMeta(db *sqlite3.Conn) (Meta, error) {
	var m Meta
	stmt, _, err := db.Prepare("SELECT built_at, watermark, source_project, tool_version FROM snapshot_meta")
	if err != nil {
		return m, errors.Wrap(err, "preparing snapshot_meta read")
	}
	defer stmt.Close()
	if !stmt.Step() {
		if err := stmt.Err(); err != nil {
			return m, errors.Wrap(err, "reading snapshot_meta")
		}
		return m, errors.New("snapshot_meta is empty")
	}
	for i, dst := range []*time.Time{&m.BuiltAt, &m.Watermark} {
		if stmt.ColumnType(i) != sqlite3.NULL {
			t, err := time.Parse(sqlitex.TimeFormat, stmt.ColumnText(i))
			if err != nil {
				return m, errors.Wrap(err, "parsing snapshot_meta time")
			}
			*dst = t
		}
	}
	m.SourceProject = stmt.ColumnText(2)
	m.ToolVersion = stmt.ColumnText(3)
	return m, nil
}
