// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package signals

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// SchemaVersion identifies the signal database layout. Bump it on any
// change that would mislead an older reader, cutting consumers over by
// artifact name as with the snapshot database.
const SchemaVersion = 1

// Object is the destination object name of the signal database, stored
// gzip-compressed as its name says. The name is stable within an era: each
// publish replaces it wholesale.
var Object = fmt.Sprintf("signals-v%d.db.gz", SchemaVersion)

// Table names in the signal database.
const (
	TablePackageSignals = "package_signals"
	TableVersionSignals = "version_signals"
)

const schema = `
CREATE TABLE package_signals(
	ecosystem TEXT NOT NULL, package TEXT NOT NULL,
	dependents INTEGER NOT NULL, prevalence REAL NOT NULL, score REAL NOT NULL,
	PRIMARY KEY(ecosystem, package)) WITHOUT ROWID;
CREATE TABLE version_signals(
	ecosystem TEXT NOT NULL, package TEXT NOT NULL, version TEXT NOT NULL,
	dependents INTEGER NOT NULL, prevalence REAL NOT NULL,
	published TEXT, artifact TEXT,
	PRIMARY KEY(ecosystem, package, version)) WITHOUT ROWID;
CREATE INDEX package_signals_score ON package_signals(ecosystem, score);
CREATE TABLE signal_meta(built_at TEXT, tool_version TEXT);
`

// Meta is the single-row provenance record of a signal database, stored in
// its signal_meta table. The schema era lives in the file header instead
// (sqlitex.Version).
type Meta struct {
	BuiltAt     time.Time // when the build began
	ToolVersion string    // builder binary version
}

// ReadMeta loads the database's meta row.
func ReadMeta(db *sqlite3.Conn) (Meta, error) {
	var m Meta
	stmt, _, err := db.Prepare("SELECT built_at, tool_version FROM signal_meta")
	if err != nil {
		return m, errors.Wrap(err, "preparing signal_meta read")
	}
	defer stmt.Close()
	if !stmt.Step() {
		if err := stmt.Err(); err != nil {
			return m, errors.Wrap(err, "reading signal_meta")
		}
		return m, errors.New("signal_meta is empty")
	}
	if stmt.ColumnType(0) != sqlite3.NULL {
		if m.BuiltAt, err = time.Parse(sqlitex.TimeFormat, stmt.ColumnText(0)); err != nil {
			return m, errors.Wrap(err, "parsing signal_meta built_at")
		}
	}
	m.ToolVersion = stmt.ColumnText(1)
	return m, nil
}

// Build writes a signal database at path from the priority exports:
// package rows joined per package via JoinSignals and one
// version_signals row per version-level prevalence record, carrying its
// publication date and artifact name when the export provides them.
// Returns per-table row counts.
func Build(path string, prevs []PrevalenceRecord, meta Meta) (map[string]int, error) {
	db, err := sqlite3.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "creating database")
	}
	defer db.Close()
	if err := db.Exec(schema); err != nil {
		return nil, errors.Wrap(err, "creating schema")
	}
	if err := sqlitex.SetVersion(db, SchemaVersion); err != nil {
		return nil, err
	}
	if err := db.Exec("BEGIN"); err != nil {
		return nil, err
	}
	pkgs := JoinSignals(prevs)
	pstmt, _, err := db.Prepare("INSERT INTO package_signals VALUES(?,?,?,?,?)")
	if err != nil {
		return nil, err
	}
	defer pstmt.Close()
	for _, s := range pkgs {
		pstmt.BindText(1, s.Ecosystem)
		pstmt.BindText(2, s.Package)
		pstmt.BindInt64(3, s.Dependents)
		pstmt.BindFloat(4, s.Prevalence)
		pstmt.BindFloat(5, s.Score)
		if err := pstmt.Exec(); err != nil {
			return nil, errors.Wrap(err, "inserting package signal")
		}
	}
	vstmt, _, err := db.Prepare("INSERT INTO version_signals VALUES(?,?,?,?,?,?,?)")
	if err != nil {
		return nil, err
	}
	defer vstmt.Close()
	versions := 0
	for _, r := range prevs {
		if r.Version == "" {
			continue
		}
		versions++
		vstmt.BindText(1, r.Ecosystem)
		vstmt.BindText(2, r.Package)
		vstmt.BindText(3, r.Version)
		vstmt.BindInt64(4, r.Dependents)
		vstmt.BindFloat(5, r.Prevalence)
		// Absent metadata is NULL, not a zero time or empty string.
		if r.Published.IsZero() {
			vstmt.BindNull(6)
		} else {
			vstmt.BindText(6, sqlitex.TimeColumn(r.Published))
		}
		if r.Artifact == "" {
			vstmt.BindNull(7)
		} else {
			vstmt.BindText(7, r.Artifact)
		}
		if err := vstmt.Exec(); err != nil {
			return nil, errors.Wrap(err, "inserting version signal")
		}
	}
	mstmt, _, err := db.Prepare("INSERT INTO signal_meta VALUES(?,?)")
	if err != nil {
		return nil, err
	}
	defer mstmt.Close()
	if meta.BuiltAt.IsZero() {
		mstmt.BindNull(1)
	} else {
		mstmt.BindText(1, sqlitex.TimeColumn(meta.BuiltAt))
	}
	mstmt.BindText(2, meta.ToolVersion)
	if err := mstmt.Exec(); err != nil {
		return nil, errors.Wrap(err, "writing signal_meta")
	}
	if err := db.Exec("COMMIT"); err != nil {
		return nil, err
	}
	return map[string]int{TablePackageSignals: len(pkgs), TableVersionSignals: versions}, nil
}

// PackageSignals reads every package row from an opened signal database.
func PackageSignals(db *sqlite3.Conn) ([]PackageSignal, error) {
	stmt, _, err := db.Prepare(`SELECT ecosystem, package, dependents, prevalence, score
		FROM package_signals ORDER BY ecosystem, package`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	var out []PackageSignal
	for stmt.Step() {
		out = append(out, PackageSignal{
			Ecosystem:  stmt.ColumnText(0),
			Package:    stmt.ColumnText(1),
			Dependents: stmt.ColumnInt64(2),
			Prevalence: stmt.ColumnFloat(3),
			Score:      stmt.ColumnFloat(4),
		})
	}
	return out, stmt.Err()
}

// Fetch downloads the signal database published under dest into dir and
// returns its local path, refusing a database from a different schema era.
func Fetch(dest billy.Basic, dir string) (string, error) {
	path := filepath.Join(dir, "signals.db")
	if err := sqlitex.Fetch(dest, Object, path); err != nil {
		return "", errors.Wrapf(err, "downloading %s", Object)
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		return "", errors.Wrap(err, "opening signal database")
	}
	defer db.Close()
	if err := sqlitex.CheckVersion(db, SchemaVersion); err != nil {
		return "", err
	}
	return path, nil
}
