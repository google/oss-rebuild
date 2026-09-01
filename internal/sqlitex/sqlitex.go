// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package sqlitex holds the conventions shared by the SQLite databases this
// repository publishes: the schema era recorded in the file header, the TEXT
// encoding of time columns, and the gzip copy that moves a built database
// between local disk and a billy filesystem.
package sqlitex

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// TimeFormat is the TEXT encoding for time columns: UTC with fixed-width
// millisecond precision, so lexicographic comparison (what SQLite's ORDER BY
// and range operators do on TEXT) equals chronological order. It matches
// strftime('%Y-%m-%dT%H:%M:%fZ', ...), which extraction expressions use to
// normalize Go's variable-precision encoding. NULL means the timestamp was
// not recorded or did not parse.
const TimeFormat = "2006-01-02T15:04:05.000Z07:00"

// TimeColumn returns t in the TEXT encoding time columns use, for binding
// comparisons against them.
func TimeColumn(t time.Time) string { return t.UTC().Format(TimeFormat) }

// Version reads the schema era recorded in the database header (PRAGMA
// user_version). A database that never had one set reads zero.
func Version(db *sqlite3.Conn) (int, error) {
	stmt, _, err := db.Prepare("PRAGMA user_version")
	if err != nil {
		return 0, errors.Wrap(err, "reading user_version")
	}
	defer stmt.Close()
	if !stmt.Step() {
		return 0, errors.Wrap(stmt.Err(), "reading user_version")
	}
	return stmt.ColumnInt(0), nil
}

// SetVersion records the schema era in the database header.
func SetVersion(db *sqlite3.Conn, v int) error {
	// PRAGMA takes no bound parameters. v is an int, so the format is safe.
	return errors.Wrap(db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)), "writing user_version")
}

// CheckVersion errors unless db's header era is exactly want. A new era
// is published under a new object name, never in place under an old one,
// so a mismatch means a mislabeled publish rather than version skew for
// a reader to tolerate.
func CheckVersion(db *sqlite3.Conn, want int) error {
	got, err := Version(db)
	if err != nil {
		return err
	}
	if got != want {
		return errors.Errorf("database is schema v%d, this binary reads v%d", got, want)
	}
	return nil
}

// Publish gzips the database file at path into fs under name. Databases
// are stored gzipped on every filesystem (they compress about 8x), local
// directories included, so the .gz name is accurate wherever the copy
// lives.
func Publish(fs billy.Basic, name, path string) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := fs.Create(name)
	if err != nil {
		return errors.Wrapf(err, "creating %s", name)
	}
	defer func() {
		if cerr := w.Close(); err == nil {
			err = errors.Wrapf(cerr, "closing %s", name)
		}
	}()
	gz := gzip.NewWriter(w)
	if _, err := io.Copy(gz, f); err != nil {
		return errors.Wrap(err, "compressing database")
	}
	return errors.Wrap(gz.Close(), "closing gzip stream")
}

// Fetch gunzips the published database fs holds under name into a local
// file at dest.
func Fetch(fs billy.Basic, name, dest string) error {
	r, err := fs.Open(name)
	if err != nil {
		return errors.Wrapf(err, "opening %s", name)
	}
	defer r.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return errors.Wrap(err, "opening gzip stream")
	}
	defer gz.Close()
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, gz); err != nil {
		f.Close()
		return errors.Wrap(err, "decompressing database")
	}
	return f.Close()
}
