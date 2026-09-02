// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/internal/sqlitex"
)

func TestReadSignals(t *testing.T) {
	built := filepath.Join(t.TempDir(), "signals.db")
	if _, err := signals.Build(built,
		[]signals.PrevalenceRecord{
			{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8},
			// Version rows publish but only package rows reach the snapshot.
			{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Dependents: 60, Prevalence: 0.75},
		},
		signals.Meta{}); err != nil {
		t.Fatalf("building signal database: %v", err)
	}
	pub := memfs.New()
	if err := sqlitex.Publish(pub, signals.Object, built); err != nil {
		t.Fatalf("publishing signal database: %v", err)
	}
	got, err := readSignals(pub)
	if err != nil {
		t.Fatalf("readSignals: %v", err)
	}
	want := []signals.PackageSignal{
		{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8, Score: 0.8},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("signals mismatch (-want +got):\n%s", diff)
	}
	if empty, err := readSignals(nil); err != nil || empty != nil {
		t.Errorf("readSignals(nil) = (%v, %v), want no rows and no error", empty, err)
	}
}
