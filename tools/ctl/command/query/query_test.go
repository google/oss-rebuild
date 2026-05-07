// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

type mockReader struct {
	rundex.Reader
	runs     []rundex.Run
	rebuilds []rundex.Rebuild
}

func (m *mockReader) FetchRuns(ctx context.Context, opts rundex.FetchRunsOpts) ([]rundex.Run, error) {
	return m.runs, nil
}

func (m *mockReader) FetchRebuilds(ctx context.Context, req *rundex.FetchRebuildRequest) ([]rundex.Rebuild, error) {
	return m.rebuilds, nil
}

func TestHandler(t *testing.T) {
	ctx := context.Background()
	out := &bytes.Buffer{}
	deps := &Deps{
		IO: cli.IO{Out: out},
		FilesystemReaderFn: func() rundex.Reader {
			return &mockReader{
				runs: []rundex.Run{
					{Run: schema.Run{ID: "run1", BenchmarkName: "bench1"}},
				},
				rebuilds: []rundex.Rebuild{
					{
						RebuildAttempt: schema.RebuildAttempt{
							Ecosystem: "npm",
							Package:   "pkg1",
							Version:   "1.0.0",
							RunID:     "run1",
							Success:   true,
						},
					},
				},
			}
		},
	}

	cfg := Config{
		query: "SELECT ecosystem, package, version FROM rebuilds",
	}

	_, err := Handler(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("Handler failed: %v", err)
	}

	got := out.String()
	want := "ecosystem  package  version\nnpm        pkg1     1.0.0\n"
	if got != want {
		t.Errorf("Unexpected output:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestHandler_Runs(t *testing.T) {
	ctx := context.Background()
	out := &bytes.Buffer{}
	deps := &Deps{
		IO: cli.IO{Out: out},
		FilesystemReaderFn: func() rundex.Reader {
			return &mockReader{
				runs: []rundex.Run{
					{
						Run: schema.Run{
							ID:            "run1",
							BenchmarkName: "bench1",
							BenchmarkHash: "hash1",
						},
						Type: schema.AttestMode,
					},
				},
			}
		},
	}

	cfg := Config{
		query: "SELECT id, benchmark FROM runs",
	}

	_, err := Handler(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("Handler failed: %v", err)
	}

	got := out.String()
	want := "id    benchmark\nrun1  bench1\n"
	if got != want {
		t.Errorf("Unexpected output for runs table:\ngot:  %q\nwant: %q", got, want)
	}
}
