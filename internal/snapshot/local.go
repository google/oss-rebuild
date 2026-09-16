// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"time"

	"github.com/google/oss-rebuild/internal/docdb"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// LocalDB is a read-write snapshot database on the local filesystem: the
// same layout the rollup publishes, but owned and written in place by a
// local tool (e.g. a benchmark run) with no Firestore involved. Concurrent
// processes coordinate through SQLite's own file locking.
type LocalDB struct {
	Conn *sqlite3.Conn
}

// OpenLocal opens or creates a snapshot database at path. Missing doc
// tables are created, so a fresh path yields an empty writable database. A
// database from a different schema era is refused: rebuilding it from its
// raw documents is the only migration.
func OpenLocal(path string) (*LocalDB, error) {
	db, err := sqlite3.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "opening database")
	}
	fail := func(err error) (*LocalDB, error) {
		db.Close()
		return nil, err
	}
	if err := db.Exec("PRAGMA busy_timeout = 10000"); err != nil {
		return fail(errors.Wrap(err, "setting busy timeout"))
	}
	// Local connections materialize derived tables via Refresh, so they
	// need the collations the derived queries use, unlike pure readers.
	if err := registerCollations(db); err != nil {
		return fail(err)
	}
	if err := docdb.EnsureDocTables(db, Tables()); err != nil {
		return fail(err)
	}
	version, err := sqlitex.Version(db)
	if err != nil {
		return fail(err)
	}
	if version == 0 {
		// No era yet: a fresh database, stamped with this one.
		if err := sqlitex.SetVersion(db, SchemaVersion); err != nil {
			return fail(err)
		}
		if err := WriteMeta(db, Meta{BuiltAt: time.Now().UTC(), SourceProject: "local"}); err != nil {
			return fail(errors.Wrap(err, "stamping meta"))
		}
	} else if err := sqlitex.CheckVersion(db, SchemaVersion); err != nil {
		return fail(err)
	}
	return &LocalDB{Conn: db}, nil
}

// Refresh rematerializes the derived tables from the doc rows written so
// far and re-stamps the meta build time.
func (l *LocalDB) Refresh() (map[string]int, error) {
	counts, err := refreshDerived(l.Conn)
	if err != nil {
		return nil, err
	}
	meta, err := ReadMeta(l.Conn)
	if err != nil {
		return nil, errors.Wrap(err, "reading meta")
	}
	meta.BuiltAt = time.Now().UTC()
	return counts, WriteMeta(l.Conn, meta)
}

func (l *LocalDB) Close() error { return l.Conn.Close() }
