// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
)

// memAnalytics serves one in-memory snapshot database.
type memAnalytics struct{ db *sqlite3.Conn }

func (m memAnalytics) Query(f func(*sqlite3.Conn) error) error { return f(m.db) }
func (m memAnalytics) Freshness() time.Time                    { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }

func newMemAnalytics(t *testing.T, stmts ...string) memAnalytics {
	t.Helper()
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range stmts {
		if err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return memAnalytics{db}
}
