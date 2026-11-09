// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package docdb

import (
	"fmt"
	"hash/fnv"
	"io"
	"strings"

	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// Scope narrows a derived table's refresh after a segment to the rows the
// segment's documents can change, for a table whose whole re-derivation
// would scan every document. Rows is a predicate over the table's columns
// selecting those rows. Views shadow the doc tables the query reads, each a
// SELECT over main.<table> yielding every row that contributes to a selected
// row. A view may yield more, since the query's result is filtered by Rows
// before it lands. Keys materialize key sets the predicate and views share,
// as temp tables named touched_<Name>, in order and before the views. All
// three can read the touched_<doc table> tables the apply fills with each
// applied document's primary key.
// NOTE: Rows must select every row whose inputs a touched document reaches,
// through joins included, and each view must be complete for them. A
// predicate that misses rows leaves them stale until the next whole build.
type Scope struct {
	Keys  []Key
	Rows  string
	Views []View
}

// Key is a key set a scoped refresh materializes, Query selecting its rows.
type Key struct{ Name, Query string }

// View is a doc table a scoped refresh shadows, Query selecting its rows
// from main.<Table>.
type View struct{ Table, Query string }

// Unrefreshed is the scope of a derived table a segment leaves as the last
// whole build left it, for one only a whole build can afford.
var Unrefreshed = &Scope{Rows: "0"}

// touchedTable names the temp table holding a key set.
func touchedTable(name string) string { return "touched_" + name }

// createTouchedSQL creates a doc table's touched key table, keyed like it.
func createTouchedSQL(td TableDef) string {
	cols := make([]string, 0, len(td.PK))
	for _, c := range td.Cols {
		for _, p := range td.PK {
			if c.Name == p {
				cols = append(cols, c.Name+" "+c.Type)
			}
		}
	}
	return "CREATE TEMP TABLE " + touchedTable(td.Name) + " (" + strings.Join(cols, ", ") +
		", PRIMARY KEY (" + strings.Join(td.PK, ", ") + ")) WITHOUT ROWID"
}

// touchSQL records the bound document's primary key in its table's touched
// key table.
func touchSQL(td TableDef) string {
	exprs := make([]string, 0, len(td.PK))
	for _, p := range td.PK {
		for _, c := range td.Cols {
			if c.Name == p {
				exprs = append(exprs, c.Expr)
			}
		}
	}
	return "INSERT OR IGNORE INTO " + touchedTable(td.Name) + " (" + strings.Join(td.PK, ", ") +
		") SELECT " + strings.Join(exprs, ", ")
}

// Derive drops and rematerializes every derived table among defs from the
// doc rows in db, in definition order so a derived table may build on an
// earlier one, returning the row counts. It stamps db as built from defs.
func Derive(db *sqlite3.Conn, defs []TableDef) (map[string]int, error) {
	counts := make(map[string]int)
	for _, td := range defs {
		if td.Query == "" {
			continue
		}
		n, err := rederive(db, td)
		if err != nil {
			return nil, err
		}
		counts[td.Name] = n
	}
	return counts, stamp(db, defs)
}

// stamp records defs' fingerprint as db's application_id, so a reader can
// tell whether db was built from the table definitions it holds.
func stamp(db *sqlite3.Conn, defs []TableDef) error {
	return errors.Wrap(db.Exec(fmt.Sprintf("PRAGMA application_id = %d", fingerprint(defs))), "stamping table definitions")
}

// fingerprint identifies the shape defs give a database: each table's CREATE
// statement or query and its indexes.
func fingerprint(defs []TableDef) int32 {
	h := fnv.New32a()
	for _, td := range defs {
		if td.Query != "" {
			io.WriteString(h, td.Name+" AS "+td.Query+"\n")
		} else {
			io.WriteString(h, createTableSQL(td)+"\n")
		}
		for _, idx := range td.Indexes {
			io.WriteString(h, createIndexSQL(td.Name, idx)+"\n")
		}
	}
	return int32(h.Sum32())
}

func rederive(db *sqlite3.Conn, td TableDef) (int, error) {
	if err := db.Exec("DROP TABLE IF EXISTS " + td.Name); err != nil {
		return 0, errors.Wrapf(err, "dropping %s", td.Name)
	}
	n, err := StoreQuery(db, td)
	return n, errors.Wrapf(err, "materializing %s", td.Name)
}

// deriveTouched refreshes every derived table once the touched key tables
// hold the applied documents' keys: a scoped table for the rows its Scope
// selects, any other whole.
func deriveTouched(db *sqlite3.Conn, defs []TableDef) error {
	for _, td := range defs {
		if td.Query == "" {
			continue
		}
		if td.Scope == nil {
			if _, err := rederive(db, td); err != nil {
				return err
			}
			continue
		}
		if td.Scope == Unrefreshed {
			continue
		}
		if err := refreshScoped(db, td); err != nil {
			return errors.Wrapf(err, "refreshing %s", td.Name)
		}
	}
	return nil
}

// refreshScoped deletes the rows td's Scope selects and re-derives them
// with the doc tables shadowed by its views. The temp schema resolves
// unqualified names first, so the query reads the views unchanged.
func refreshScoped(db *sqlite3.Conn, td TableDef) error {
	s := td.Scope
	var stmts []string
	for _, k := range s.Keys {
		stmts = append(stmts, "CREATE TEMP TABLE "+touchedTable(k.Name)+" AS "+k.Query)
	}
	for _, v := range s.Views {
		stmts = append(stmts, "CREATE TEMP VIEW "+v.Table+" AS "+v.Query)
	}
	stmts = append(stmts,
		"DELETE FROM "+td.Name+" WHERE "+s.Rows,
		"INSERT INTO "+td.Name+" SELECT * FROM ("+td.Query+") WHERE "+s.Rows)
	for _, v := range s.Views {
		stmts = append(stmts, "DROP VIEW temp."+v.Table)
	}
	for _, k := range s.Keys {
		stmts = append(stmts, "DROP TABLE "+touchedTable(k.Name))
	}
	for _, sql := range stmts {
		if err := db.Exec(sql); err != nil {
			return errors.Wrap(err, strings.Join(strings.Fields(sql)[:3], " "))
		}
	}
	return nil
}
