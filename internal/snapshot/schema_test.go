// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// This file guards the snapshot schema: TestSchemaShapeIsGolden compares the
// registry's materialized column shapes against the committed golden for the
// current SchemaVersion. When it fails:
//
// (a) The golden needs updating (additive change: new columns or tables):
//
//	go test ./internal/snapshot -run TestSchemaShapeIsGolden -update
//
// (b) SchemaVersion needs bumping (breaking change: a column or table
// removed, renamed, retyped, or re-keyed): increment SchemaVersion in
// tables.go, then run the same command to create the new era's golden.

package snapshot

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/ncruces/go-sqlite3"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden schema shape for the current SchemaVersion")

// schemaShape captures every registry table's materialized column set, as
// column name -> "TYPE", "TYPE pk=N", or "" for the derived tables' untyped
// expression columns: the observable surface readers and their SQL depend
// on, deliberately excluding column order and generation expressions.
// We use table_xinfo, not table_info, since only the former lists generated
// columns, which are most of the doc tables' surface.
func schemaShape(t *testing.T, db *sqlite3.Conn) map[string]map[string]string {
	t.Helper()
	shape := map[string]map[string]string{}
	for _, td := range Tables() {
		stmt, _, err := db.Prepare("PRAGMA table_xinfo(" + td.Name + ")")
		if err != nil {
			t.Fatalf("table_xinfo(%s): %v", td.Name, err)
		}
		cols := map[string]string{}
		for stmt.Step() {
			s := stmt.ColumnText(2)
			if pk := stmt.ColumnInt(5); pk > 0 {
				s = strings.TrimSpace(s + fmt.Sprintf(" pk=%d", pk))
			}
			cols[stmt.ColumnText(1)] = s
		}
		if err := stmt.Close(); err != nil {
			t.Fatalf("table_xinfo(%s): %v", td.Name, err)
		}
		shape[td.Name] = cols
	}
	return shape
}

// isBreaking reports whether current drops anything golden promises: a
// column removed, renamed, retyped, or repositioned within the primary
// key, or a whole table gone. Readers compiled against golden cannot
// survive these. Purely additive growth is not breaking.
func isBreaking(golden, current map[string]map[string]string) bool {
	for name, gcols := range golden {
		for col, shape := range gcols {
			if cur, ok := current[name][col]; !ok || cur != shape {
				return true
			}
		}
	}
	return false
}

// readGolden loads a committed schema shape.
func readGolden(path string) (map[string]map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var shape map[string]map[string]string
	if err := json.Unmarshal(b, &shape); err != nil {
		return nil, err
	}
	return shape, nil
}

// TestSchemaShapeIsGolden materializes the registry into an empty database
// and compares the resulting column shapes against the golden file for the
// current SchemaVersion. The file comment gives the update workflows.
// -update refuses to rewrite the current era's golden incompatibly, so the
// version bump cannot be skipped by reflex. Superseded goldens stay
// committed as the record of what readers of their era consume.
func TestSchemaShapeIsGolden(t *testing.T) {
	db, _ := fill(t, &fakeSource{})
	current := schemaShape(t, db)
	path := filepath.Join("testdata", fmt.Sprintf("schema_v%d.json", SchemaVersion))
	if *updateGolden {
		if prior, err := readGolden(path); err == nil && isBreaking(prior, current) {
			t.Fatalf("refusing to rewrite the v%d golden with a breaking change: a column or key it promises was removed, renamed, or retyped. Bump SchemaVersion; -update then creates the new era's golden.", SchemaVersion)
		}
		b, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	golden, err := readGolden(path)
	if err != nil {
		t.Fatalf("no golden for schema v%d (%v): if you bumped SchemaVersion, create it with `go test -run TestSchemaShapeIsGolden -update`", SchemaVersion, err)
	}
	if diff := cmp.Diff(golden, current); diff != "" {
		if isBreaking(golden, current) {
			t.Fatalf("schema shape changed incompatibly for v%d (-golden +current):\n%s\nA column or key that v%d readers depend on was removed, renamed, or retyped: bump SchemaVersion and create the new golden with -update.", SchemaVersion, diff, SchemaVersion)
		}
		t.Fatalf("schema shape grew (-golden +current):\n%s\nAdditive changes are compatible: regenerate the golden with `go test -run TestSchemaShapeIsGolden -update`.", diff)
	}
}
