// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"encoding/json"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/google/oss-rebuild/internal/docdb"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/ncruces/go-sqlite3"
)

// saturationTime keeps every time field non-zero: a zero time normalizes to
// NULL by design and would mask a broken path.
var saturationTime = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// saturate sets every settable leaf reachable from v to a non-zero value,
// allocating pointers along the way (a oneof saturates every arm).
func saturate(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		saturate(v.Elem())
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(saturationTime))
			return
		}
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() {
				saturate(f)
			}
		}
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte(`"x"`)) // json.RawMessage stays valid JSON
			return
		}
		e := reflect.New(v.Type().Elem()).Elem()
		saturate(e)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), e))
	case reflect.Map:
		k := reflect.New(v.Type().Key()).Elem()
		e := reflect.New(v.Type().Elem()).Elem()
		saturate(k)
		saturate(e)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(k, e)
		v.Set(m)
	}
}

var extractionPathRE = regexp.MustCompile(`\$(?:\.[A-Za-z0-9_]+)+`)

// TestExtractionPathsAreUnique rejects two columns of a table sharing an
// extraction expression: a duplicated path is a copy-paste error that the
// saturation test cannot see, since both columns read non-NULL. A crossed
// pairing (the right paths on the wrong columns) has no mechanical check
// at all, which is why the registry warns reviewers to check the pairing.
func TestExtractionPathsAreUnique(t *testing.T) {
	for _, td := range Tables() {
		if td.Query != "" {
			continue
		}
		type namedExpr struct{ name, expr string }
		var all []namedExpr
		for _, c := range td.Cols {
			all = append(all, namedExpr{c.Name, c.Expr})
		}
		for _, g := range td.GenCols {
			all = append(all, namedExpr{g.Name, g.Expr})
		}
		seen := map[string]string{}
		for _, ne := range all {
			if len(extractionPathRE.FindAllString(ne.expr, -1)) == 0 {
				continue
			}
			if prev, dup := seen[ne.expr]; dup {
				t.Errorf("%s: columns %s and %s share an expression", td.Name, prev, ne.name)
			}
			seen[ne.expr] = ne.name
		}
	}
}

func saturated[T any]() T {
	v := new(T)
	saturate(reflect.ValueOf(v).Elem())
	return *v
}

// TestSaturatedDocumentFillsEveryColumn guards the registry's extraction
// paths against the source structs. The paths reference the structs' JSON
// encoding by string, and a renamed field or a newly added json tag makes a
// path extract NULL without any other symptom, indistinguishable from data
// that was never measured. A fully populated source document must therefore
// read non-NULL in every declared and generated column.
func TestSaturatedDocumentFillsEveryColumn(t *testing.T) {
	docs := map[string]any{
		"attempts":         saturated[schema.RebuildAttempt](),
		"runs":             saturated[schema.Run](),
		"agent_sessions":   saturated[schema.AgentSession](),
		"agent_iterations": saturated[schema.AgentIteration](),
		"scratch_vms":      saturated[schema.Scratch](),
		"scratch_execs":    saturated[schema.ScratchExec](),
		"repo_metrics":     saturated[schema.RepoMetrics](),
	}
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	docTables := 0
	for _, td := range Tables() {
		if td.Query != "" {
			continue
		}
		docTables++
		doc, ok := docs[td.Name]
		if !ok {
			t.Fatalf("no saturated document for doc table %s", td.Name)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshaling %s document: %v", td.Name, err)
		}
		if err := docdb.StoreDocs(db, td, []json.RawMessage{raw}); err != nil {
			t.Fatalf("storing %s: %v", td.Name, err)
		}
		types := map[string]string{"raw": "TEXT"}
		for _, c := range td.Cols {
			types[c.Name] = c.Type
		}
		for _, g := range td.GenCols {
			types[g.Name] = g.Type
		}
		for col, decl := range types {
			assertCount(t, db, "SELECT count(*) FROM "+td.Name+" WHERE "+col+" IS NOT NULL", "1")
			// SQLite stores whatever the expression yields regardless of
			// the declared affinity, so a path pointed at the wrong field
			// can fill a numeric column with text. Pin the stored type.
			// SQLite also accepts any declared type name (affinity matches
			// by substring), so an unknown declaration fails here rather
			// than silently skipping the pin.
			want, ok := map[string]string{"TEXT": "text", "INTEGER": "integer", "REAL": "real"}[decl]
			if !ok {
				t.Errorf("%s.%s declares type %s, want TEXT, INTEGER, or REAL", td.Name, col, decl)
				continue
			}
			if got := queryText(t, db, "SELECT typeof("+col+") FROM "+td.Name); got != want {
				t.Errorf("%s.%s stores %s, declared %s", td.Name, col, got, decl)
			}
		}
	}
	// Both directions: every doc table has a fixture (checked above), and
	// every fixture names a doc table, so a renamed table cannot leave a
	// stale entry behind.
	if docTables != len(docs) {
		t.Fatalf("saturated %d documents, registry declares %d doc tables", len(docs), docTables)
	}
}

// TestSkeletonDocumentsKeepGuardedColumnsDefined stores minimal hand-written
// documents: keys present, every optional field absent, unlike anything the
// Go marshaler produces today. Guarded columns must still read defined
// values, since SQL NULL propagates through operators (NULL OR 0 is NULL,
// not 0) and a column that goes NULL on sparse documents is
// indistinguishable from unmeasured data.
func TestSkeletonDocumentsKeepGuardedColumnsDefined(t *testing.T) {
	skeletons := map[string]string{
		"attempts":         `{"Ecosystem":"pypi","Package":"p","Version":"1","Artifact":"a.whl","RunID":"r1"}`,
		"runs":             `{"ID":"r1"}`,
		"agent_sessions":   `{"ID":"s1"}`,
		"agent_iterations": `{"SessionID":"s1","Number":1}`,
		"scratch_vms":      `{"id":"sc1"}`,
		"scratch_execs":    `{"id":"e1"}`,
		"repo_metrics":     `{"uri":"u"}`,
	}
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	docTables := 0
	for _, td := range Tables() {
		if td.Query != "" {
			continue
		}
		docTables++
		doc, ok := skeletons[td.Name]
		if !ok {
			t.Fatalf("no skeleton document for doc table %s", td.Name)
		}
		if err := docdb.StoreDocs(db, td, []json.RawMessage{json.RawMessage(doc)}); err != nil {
			t.Fatalf("storing %s: %v", td.Name, err)
		}
	}
	if docTables != len(skeletons) {
		t.Fatalf("wrote %d skeletons, registry declares %d doc tables", len(skeletons), docTables)
	}
	assertCount(t, db, `SELECT count(*) FROM attempts WHERE status = 'FAILURE' AND success = 0
		AND mechanism = '' AND strategy_type = '' AND has_costs = 0 AND cost_builder_seconds IS NULL`, "1")
	assertCount(t, db, "SELECT count(*) FROM scratch_vms WHERE vm_seconds = 0.0", "1")
	assertCount(t, db, "SELECT count(*) FROM scratch_execs WHERE exec_seconds IS NULL", "1")
}
