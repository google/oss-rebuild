// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"google.golang.org/api/iterator"
)

func TestRankByEcosystem(t *testing.T) {
	// Package and version rows share each ecosystem's denominator, its most
	// depended-on package, so a version never outscores its package.
	got := scoreByEcosystem([]signals.PrevalenceRecord{
		{Ecosystem: "npm", Package: "rarely-used", Dependents: 1},
		{Ecosystem: "rubygems", Package: "rack", Dependents: 50},
		{Ecosystem: "npm", Package: "lodash", Dependents: 9000},
		{Ecosystem: "npm", Package: "lodash", Version: "4.17.21", Dependents: 8000},
		{Ecosystem: "npm", Package: "lodash", Version: "3.10.1", Dependents: 400},
		{Ecosystem: "npm", Package: "rarely-used", Version: "1.0.0", Dependents: 1},
		{Ecosystem: "", Package: "dropped-no-ecosystem", Dependents: 5},
		{Ecosystem: "npm", Package: "", Dependents: 5},
	})

	npm := func(dependents int64) float64 { return math.Log1p(float64(dependents)) / math.Log1p(9000) }
	want := []signals.PrevalenceRecord{
		{Ecosystem: "npm", Package: "lodash", Dependents: 9000, Prevalence: 1.0},
		{Ecosystem: "npm", Package: "rarely-used", Dependents: 1, Prevalence: npm(1)},
		{Ecosystem: "rubygems", Package: "rack", Dependents: 50, Prevalence: 1.0},
		{Ecosystem: "npm", Package: "lodash", Version: "4.17.21", Dependents: 8000, Prevalence: npm(8000)},
		{Ecosystem: "npm", Package: "lodash", Version: "3.10.1", Dependents: 400, Prevalence: npm(400)},
		{Ecosystem: "npm", Package: "rarely-used", Version: "1.0.0", Dependents: 1, Prevalence: npm(1)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ranked records mismatch (-want +got):\n%s", diff)
	}
}

func TestWriteJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prevalence.jsonl")
	rows := []signals.PrevalenceRecord{
		{Ecosystem: "npm", Package: "lodash", Dependents: 9000, Prevalence: 1.0},
		{Ecosystem: "npm", Package: "lodash", Version: "4.17.21", Dependents: 8000, Prevalence: 1.0},
	}
	files := memfs.New()
	if err := writeJSONL(files, path, rows); err != nil {
		t.Fatalf("writeJSONL: %v", err)
	}
	f, err := files.Open(path)
	if err != nil {
		t.Fatalf("opening export: %v", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}
	var got []signals.PrevalenceRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	for dec.More() {
		var r signals.PrevalenceRecord
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decoding line: %v", err)
		}
		got = append(got, r)
	}
	if diff := cmp.Diff(rows, got); diff != "" {
		t.Errorf("round-tripped records mismatch (-want +got):\n%s", diff)
	}
}

// fakeRows feeds canned rows into the caller's row struct by field name.
type fakeRows struct {
	rows []map[string]any
	i    int
}

func (f *fakeRows) Next(dst any) error {
	if f.i >= len(f.rows) {
		return iterator.Done
	}
	d := reflect.ValueOf(dst).Elem()
	for name, val := range f.rows[f.i] {
		d.FieldByName(name).Set(reflect.ValueOf(val))
	}
	f.i++
	return nil
}

// fakeBQ answers the three prevalence queries, keyed on distinctive SQL
// fragments.
type fakeBQ struct{ snap time.Time }

func (f fakeBQ) Query(_ context.Context, sql string, _ []bigquery.QueryParameter) (rowIterator, error) {
	switch {
	case strings.Contains(sql, ".Snapshots`"):
		return &fakeRows{rows: []map[string]any{{"Time": f.snap}}}, nil
	case strings.Contains(sql, "Version AS Version"):
		return &fakeRows{rows: []map[string]any{
			{"Package": "lodash", "Version": "4.17.21", "Dependents": int64(8000)},
		}}, nil
	default:
		return &fakeRows{rows: []map[string]any{
			{"Package": "lodash", "Dependents": int64(9000)},
			{"Package": "rarely-used", "Dependents": int64(1)},
		}}, nil
	}
}

// TestPrevalenceHandler runs the whole handler against a fake query client
// and an in-memory file store: rank, score, and export land without a live
// BigQuery project.
func TestPrevalenceHandler(t *testing.T) {
	files := memfs.New()
	deps := &Deps{
		IO: cli.IO{Out: io.Discard, Err: io.Discard},
		FSFor: func(_ context.Context, uri string) (billy.Filesystem, string, error) {
			return files, uri, nil
		},
		BigQuery: func(context.Context, string) (queryClient, error) {
			return fakeBQ{snap: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
		},
	}
	cfg := prevalenceConfig{Project: "p", Ecosystems: "npm", Top: 10, TopVersions: 10, Out: "prevalence.jsonl"}
	if _, err := prevalenceHandler(context.Background(), cfg, deps); err != nil {
		t.Fatalf("prevalenceHandler: %v", err)
	}
	got, err := readJSONL[signals.PrevalenceRecord](files, "prevalence.jsonl")
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}
	want := []signals.PrevalenceRecord{
		{Ecosystem: "npm", Package: "lodash", Dependents: 9000, Prevalence: 1.0},
		{Ecosystem: "npm", Package: "rarely-used", Dependents: 1, Prevalence: math.Log1p(1) / math.Log1p(9000)},
		{Ecosystem: "npm", Package: "lodash", Version: "4.17.21", Dependents: 8000, Prevalence: math.Log1p(8000) / math.Log1p(9000)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("exported records mismatch (-want +got):\n%s", diff)
	}
}
