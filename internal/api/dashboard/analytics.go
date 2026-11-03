// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"sync"
	"time"

	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// Analytics serves the snapshot database behind the derived-trend pages
// (docdb.Cache satisfies it). A nil Analytics degrades those pages to an
// empty state without affecting the live views.
type Analytics interface {
	// Query runs f with the current database, serialized against refreshes.
	Query(f func(*sqlite3.Conn) error) error
	// Freshness is the time through which the served data is complete.
	Freshness() time.Time
}

// analyticsWindowDays bounds the recent-activity windows on the trend pages.
const analyticsWindowDays = 30

// forEachRow runs one query against the snapshot database, invoking fn per
// result row.
func forEachRow(a Analytics, sql string, fn func(*sqlite3.Stmt) error) error {
	return a.Query(func(db *sqlite3.Conn) error {
		stmt, _, err := db.Prepare(sql)
		if err != nil {
			return errors.Wrap(err, "preparing query")
		}
		defer stmt.Close()
		for stmt.Step() {
			if err := fn(stmt); err != nil {
				return err
			}
		}
		return errors.Wrap(stmt.Err(), "executing query")
	})
}

// snapshotMemo keeps a loader's result for one snapshot version, so the
// pages that share a query run it once per version. Callers treat the result
// as read-only. The Analytics must be comparable, as every implementation is.
type snapshotMemo[T any] struct {
	mu    sync.Mutex
	a     Analytics
	fresh time.Time
	val   T
}

// get returns load's result for a's current version, running load when the
// kept one is from another version or snapshot. Callers wait on one run.
func (m *snapshotMemo[T]) get(a Analytics, load func(Analytics) (T, error)) (T, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fresh := a.Freshness()
	if m.a == a && m.fresh.Equal(fresh) {
		return m.val, nil
	}
	v, err := load(a)
	if err != nil {
		return v, err
	}
	m.a, m.fresh, m.val = a, fresh, v
	return v, nil
}

// asOfLayout formats the times page footers report.
const asOfLayout = "2006-01-02 15:04:05 MST"

// asOf formats an Analytics' freshness for page footers.
func asOf(a Analytics) string {
	return a.Freshness().Format(asOfLayout)
}

// rolledUpAt formats the last rollup's watermark. Deltas advance only the doc
// tables, so a derived figure is as old as this, not as asOf.
func rolledUpAt(a Analytics) (string, error) {
	var m snapshot.Meta
	err := a.Query(func(db *sqlite3.Conn) (err error) {
		m, err = snapshot.ReadMeta(db)
		return err
	})
	if err != nil {
		return "", errors.Wrap(err, "reading the rollup watermark")
	}
	return m.Watermark.Format(asOfLayout), nil
}
