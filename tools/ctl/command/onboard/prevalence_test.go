// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/google/oss-rebuild/internal/signals"
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
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := jsonl.Encode(f, rows); err != nil {
		t.Fatalf("jsonl.Encode: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(path)
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
