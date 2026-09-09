// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"errors"
	"iter"
	"path/filepath"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/internal/sqlitex"
)

func TestReadSignals(t *testing.T) {
	published := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	built := filepath.Join(t.TempDir(), "signals.db")
	if _, err := signals.Build(built,
		[]signals.PrevalenceRecord{
			{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8},
			// Version rows publish but only package rows reach the snapshot.
			{Ecosystem: "pypi", Package: "pkgA", Version: "1.0", Dependents: 60, Prevalence: 0.75},
		},
		signals.Meta{BuiltAt: published}); err != nil {
		t.Fatalf("building signal database: %v", err)
	}
	pub := memfs.New()
	if err := sqlitex.Publish(pub, signals.Object, built); err != nil {
		t.Fatalf("publishing signal database: %v", err)
	}
	rows, builtAt, err := readSignals(pub)
	if err != nil {
		t.Fatalf("readSignals: %v", err)
	}
	got, err := iterx.Collect(rows)
	if err != nil {
		t.Fatalf("draining signals: %v", err)
	}
	want := []signals.PackageSignal{
		{Ecosystem: "pypi", Package: "pkgA", Dependents: 100, Prevalence: 0.8, Score: 0.8},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("signals mismatch (-want +got):\n%s", diff)
	}
	if !builtAt.Equal(published) {
		t.Errorf("builtAt = %v, want %v", builtAt, published)
	}
	// No published database configured reads as no signals, so a rollup
	// stays runnable before the first publish.
	if rows, _, err := readSignals(nil); err != nil {
		t.Errorf("readSignals(nil) = %v, want no error", err)
	} else if got, err := iterx.Collect(rows); err != nil || got != nil {
		t.Errorf("readSignals(nil) rows = (%v, %v), want none", got, err)
	}
}

func TestPagesResumeAfterTheLastDocument(t *testing.T) {
	doc := func(id string) *firestore.DocumentSnapshot {
		return &firestore.DocumentSnapshot{Ref: &firestore.DocumentRef{ID: id}}
	}
	ids := func(docs []*firestore.DocumentSnapshot) []string {
		var out []string
		for _, d := range docs {
			out = append(out, d.Ref.ID)
		}
		return out
	}
	// serve answers pages of size 2 from docs, recording the cursor each page
	// resumed after.
	serve := func(docs []*firestore.DocumentSnapshot, cursors *[]string) func(*firestore.DocumentSnapshot) iter.Seq2[*firestore.DocumentSnapshot, error] {
		return func(after *firestore.DocumentSnapshot) iter.Seq2[*firestore.DocumentSnapshot, error] {
			start := 0
			if after != nil {
				*cursors = append(*cursors, after.Ref.ID)
				for i, d := range docs {
					if d == after {
						start = i + 1
					}
				}
			}
			return iterx.FromSlice(docs[start:min(start+2, len(docs))])
		}
	}
	for _, tc := range []struct {
		name        string
		docs        []string
		wantCursors []string
	}{
		{name: "short last page ends the scan", docs: []string{"a", "b", "c", "d", "e"}, wantCursors: []string{"b", "d"}},
		{name: "an empty page after a full one ends the scan", docs: []string{"a", "b", "c", "d"}, wantCursors: []string{"b", "d"}},
		{name: "one short page", docs: []string{"a"}, wantCursors: nil},
		{name: "nothing", docs: nil, wantCursors: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var docs []*firestore.DocumentSnapshot
			for _, id := range tc.docs {
				docs = append(docs, doc(id))
			}
			var cursors []string
			got, err := iterx.Collect(pages(2, serve(docs, &cursors)))
			if err != nil {
				t.Fatalf("pages: %v", err)
			}
			if diff := cmp.Diff(tc.docs, ids(got)); diff != "" {
				t.Errorf("documents mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCursors, cursors); diff != "" {
				t.Errorf("cursors mismatch (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("a failed page ends the scan with its error", func(t *testing.T) {
		boom := errors.New("boom")
		var pagesRun int
		run := func(after *firestore.DocumentSnapshot) iter.Seq2[*firestore.DocumentSnapshot, error] {
			pagesRun++
			if after == nil {
				return iterx.FromSlice([]*firestore.DocumentSnapshot{doc("a"), doc("b")})
			}
			return iterx.Error[*firestore.DocumentSnapshot](boom)
		}
		var got []string
		var err error
		for d, e := range pages(2, run) {
			if e != nil {
				err = e
				break
			}
			got = append(got, d.Ref.ID)
		}
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		if diff := cmp.Diff([]string{"a", "b"}, got); diff != "" {
			t.Errorf("documents before the failure (-want +got):\n%s", diff)
		}
		if pagesRun != 2 {
			t.Errorf("pages run = %d, want 2", pagesRun)
		}
	})
}
