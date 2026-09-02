// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/ncruces/go-sqlite3"
)

func writeJSONLFile[T any](t *testing.T, path string, rows []T) {
	t.Helper()
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
}

func TestPublish(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	prev := filepath.Join(dir, "prevalence.jsonl")
	writeJSONLFile(t, prev, []signals.PrevalenceRecord{
		{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8},
		{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Dependents: 60, Prevalence: 0.75},
	})
	dest := t.TempDir()
	var out bytes.Buffer
	_, err := publishHandler(ctx, publishConfig{Prevalence: prev, Dest: "file://" + dest},
		&Deps{IO: cli.IO{Out: &out, Err: &out}})
	if err != nil {
		t.Fatalf("publishHandler: %v", err)
	}
	if !strings.Contains(out.String(), "1 packages, 1 versions") {
		t.Errorf("output = %q, want package and version counts", out.String())
	}
	path, err := signals.Fetch(osfs.New(dest), t.TempDir())
	if err != nil {
		t.Fatalf("fetching published database: %v", err)
	}
	db, err := sqlite3.Open(path)
	if err != nil {
		t.Fatalf("opening published database: %v", err)
	}
	defer db.Close()
	rows, err := signals.PackageSignals(db)
	if err != nil {
		t.Fatalf("PackageSignals: %v", err)
	}
	if len(rows) != 1 || rows[0].Score != 0.8 {
		t.Errorf("published rows = %+v, want one pkgA row scored 0.8", rows)
	}
}
