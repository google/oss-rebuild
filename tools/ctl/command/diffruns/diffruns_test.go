// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffruns

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
)

func makeRebuild(eco, pkg, ver, art string, success bool, msg string, strategy *schema.StrategyOneOf) rundex.Rebuild {
	r := rundex.Rebuild{
		RebuildAttempt: schema.RebuildAttempt{
			Ecosystem: eco,
			Package:   pkg,
			Version:   ver,
			Artifact:  art,
			Success:   success,
			Message:   msg,
		},
	}
	if strategy != nil {
		r.Strategy = *strategy
	}
	return r
}

func strategyOneOf(repo, ref, npmVer string) *schema.StrategyOneOf {
	return &schema.StrategyOneOf{
		NPMPackBuild: &npm.NPMPackBuild{
			Location:   rebuild.Location{Repo: repo, Ref: ref},
			NPMVersion: npmVer,
		},
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "missing run-a",
			cfg:     Config{RunB: "b"},
			wantErr: "run-a is required",
		},
		{
			name:    "missing run-b",
			cfg:     Config{RunA: "a"},
			wantErr: "run-b is required",
		},
		{
			name:    "same run",
			cfg:     Config{RunA: "a", RunB: "a"},
			wantErr: "run-a and run-b must be different",
		},
		{
			name:    "invalid format",
			cfg:     Config{RunA: "a", RunB: "b", Format: "xml"},
			wantErr: "invalid format",
		},
		{
			name:    "invalid filter",
			cfg:     Config{RunA: "a", RunB: "b", Filter: "none"},
			wantErr: "invalid filter",
		},
		{
			name: "valid config",
			cfg:  Config{RunA: "a", RunB: "b"},
		},
		{
			name: "valid with all options",
			cfg:  Config{RunA: "a", RunB: "b", Format: "detail", Filter: "regressions"},
		},
		{
			name: "valid progress filter",
			cfg:  Config{RunA: "a", RunB: "b", Filter: "progress"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestComputeDiff(t *testing.T) {
	stratA := strategyOneOf("https://github.com/foo/bar", "v1.0", "8.0.0")
	stratB := strategyOneOf("https://github.com/foo/bar", "v1.0", "9.0.0")

	tests := []struct {
		name      string
		rebuildsA []rundex.Rebuild
		rebuildsB []rundex.Rebuild
		wantType  ChangeType
		check     func(t *testing.T, result *DiffResult)
	}{
		{
			name:      "success to failure is regression",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", true, "", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "build failed", nil)},
			wantType:  Regression,
		},
		{
			name:      "failure to success is improvement",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "build failed", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", true, "", nil)},
			wantType:  Improvement,
		},
		{
			name:      "different unknown errors is changed error",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "error A", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "error B", nil)},
			wantType:  ChangedError,
		},
		{
			name:      "forward stage is progress",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "build: failed", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "compare: content mismatch", nil)},
			wantType:  Progress,
		},
		{
			name:      "same stage different message is changed error",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "build: failed", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "build: internal error", nil)},
			wantType:  ChangedError,
		},
		{
			name:      "backward stage is changed error",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "compare: content mismatch", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "build: failed", nil)},
			wantType:  ChangedError,
		},
		{
			name:      "unknown stage A is changed error",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "something weird", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "build: failed", nil)},
			wantType:  ChangedError,
		},
		{
			name:      "unknown stage B is changed error",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "build: failed", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "something weird", nil)},
			wantType:  ChangedError,
		},
		{
			name:      "both unknown stages is changed error",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "error A", nil)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", false, "error B", nil)},
			wantType:  ChangedError,
		},
		{
			name:      "different strategy is changed strategy",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", true, "", stratA)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", true, "", stratB)},
			wantType:  ChangedStrategy,
			check: func(t *testing.T, result *DiffResult) {
				if result.Diffs[0].StratDiff == "" {
					t.Fatal("expected non-empty strategy diff")
				}
			},
		},
		{
			name:      "same result is unchanged",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", true, "", stratA)},
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", true, "", stratA)},
			wantType:  Unchanged,
		},
		{
			name:      "only in A",
			rebuildsA: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", true, "", nil)},
			rebuildsB: nil,
			wantType:  OnlyInA,
			check: func(t *testing.T, result *DiffResult) {
				if result.Diffs[0].A == nil {
					t.Fatal("expected A to be non-nil")
				}
				if result.Diffs[0].B != nil {
					t.Fatal("expected B to be nil")
				}
			},
		},
		{
			name:      "only in B",
			rebuildsA: nil,
			rebuildsB: []rundex.Rebuild{makeRebuild("npm", "pkg", "1.0", "", true, "", nil)},
			wantType:  OnlyInB,
			check: func(t *testing.T, result *DiffResult) {
				if result.Diffs[0].A != nil {
					t.Fatal("expected A to be nil")
				}
				if result.Diffs[0].B == nil {
					t.Fatal("expected B to be non-nil")
				}
			},
		},
		{
			name:      "empty runs",
			rebuildsA: nil,
			rebuildsB: nil,
			wantType:  -1, // sentinel: no diffs expected
			check: func(t *testing.T, result *DiffResult) {
				if len(result.Diffs) != 0 {
					t.Fatalf("expected 0 diffs, got %d", len(result.Diffs))
				}
				if result.Summary.TotalA != 0 || result.Summary.TotalB != 0 {
					t.Fatalf("expected 0 totals, got %d/%d", result.Summary.TotalA, result.Summary.TotalB)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ComputeDiff("runA", "runB", tt.rebuildsA, tt.rebuildsB)
			if tt.wantType >= 0 {
				if len(result.Diffs) != 1 {
					t.Fatalf("expected 1 diff, got %d", len(result.Diffs))
				}
				if result.Diffs[0].Type != tt.wantType {
					t.Fatalf("expected %v, got %v", tt.wantType, result.Diffs[0].Type)
				}
			}
			if tt.check != nil {
				tt.check(t, result)
			}
		})
	}
}

func TestComputeDiffSummary(t *testing.T) {
	stratA := strategyOneOf("https://github.com/foo/bar", "v1.0", "8.0.0")
	stratB := strategyOneOf("https://github.com/foo/bar", "v1.0", "9.0.0")
	rebuildsA := []rundex.Rebuild{
		makeRebuild("npm", "a", "1.0", "", true, "", nil),                 // regression
		makeRebuild("npm", "b", "1.0", "", false, "repo: not found", nil), // improvement
		makeRebuild("npm", "c", "1.0", "", false, "build: failed", nil),   // changed error (same stage)
		makeRebuild("npm", "d", "1.0", "", true, "", stratA),              // changed strategy
		makeRebuild("npm", "e", "1.0", "", true, "", nil),                 // only in A
		makeRebuild("npm", "g", "1.0", "", true, "", nil),                 // unchanged
		makeRebuild("npm", "h", "1.0", "", false, "build: failed", nil),   // progress
	}
	rebuildsB := []rundex.Rebuild{
		makeRebuild("npm", "a", "1.0", "", false, "build: failed", nil),             // regression
		makeRebuild("npm", "b", "1.0", "", true, "", nil),                           // improvement
		makeRebuild("npm", "c", "1.0", "", false, "build: internal error", nil),     // changed error (same stage)
		makeRebuild("npm", "d", "1.0", "", true, "", stratB),                        // changed strategy
		makeRebuild("npm", "f", "1.0", "", true, "", nil),                           // only in B
		makeRebuild("npm", "g", "1.0", "", true, "", nil),                           // unchanged
		makeRebuild("npm", "h", "1.0", "", false, "compare: content mismatch", nil), // progress
	}
	result := ComputeDiff("runA", "runB", rebuildsA, rebuildsB)
	s := result.Summary

	// Change type counts.
	if s.Regressions != 1 || s.Improvements != 1 || s.ChangedErrs != 1 || s.Progress != 1 || s.ChangedStrats != 1 || s.OnlyInA != 1 || s.OnlyInB != 1 || s.Unchanged != 1 {
		t.Fatalf("unexpected change type counts: %+v", s)
	}
	// Totals and success counts.
	if s.TotalA != 7 || s.TotalB != 7 {
		t.Fatalf("expected 7/7 totals, got %d/%d", s.TotalA, s.TotalB)
	}
	if s.SuccessA != 4 || s.SuccessB != 4 {
		t.Fatalf("expected 4/4 successes, got %d/%d", s.SuccessA, s.SuccessB)
	}
	// Stage counts — Run A: repo:1, build:2 ("build: failed" x2), unknown:0.
	wantStagesA := StageCounts{0, 1, 0, 2, 0}
	if s.StageCountsA != wantStagesA {
		t.Fatalf("StageCountsA = %v, want %v", s.StageCountsA, wantStagesA)
	}
	// Stage counts — Run B: build:2 ("build: failed" + "build: internal error"), compare:1.
	wantStagesB := StageCounts{0, 0, 0, 2, 1}
	if s.StageCountsB != wantStagesB {
		t.Fatalf("StageCountsB = %v, want %v", s.StageCountsB, wantStagesB)
	}
	// Diffs should be sorted by ID.
	for i := 1; i < len(result.Diffs); i++ {
		if result.Diffs[i].ID < result.Diffs[i-1].ID {
			t.Fatalf("diffs not sorted: %s < %s", result.Diffs[i].ID, result.Diffs[i-1].ID)
		}
	}
}

func TestStrategiesEqual(t *testing.T) {
	tests := []struct {
		name  string
		a, b  *schema.StrategyOneOf
		equal bool
	}{
		{
			name:  "same strategy",
			a:     strategyOneOf("https://github.com/foo/bar", "v1.0", "8.0.0"),
			b:     strategyOneOf("https://github.com/foo/bar", "v1.0", "8.0.0"),
			equal: true,
		},
		{
			name:  "different npm version",
			a:     strategyOneOf("https://github.com/foo/bar", "v1.0", "8.0.0"),
			b:     strategyOneOf("https://github.com/foo/bar", "v1.0", "9.0.0"),
			equal: false,
		},
		{
			name:  "different ref",
			a:     strategyOneOf("https://github.com/foo/bar", "v1.0", "8.0.0"),
			b:     strategyOneOf("https://github.com/foo/bar", "v2.0", "8.0.0"),
			equal: false,
		},
		{
			name:  "zero values",
			a:     &schema.StrategyOneOf{},
			b:     &schema.StrategyOneOf{},
			equal: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := makeRebuild("npm", "pkg", "1.0", "", true, "", tt.a)
			b := makeRebuild("npm", "pkg", "1.0", "", true, "", tt.b)
			got := strategiesEqual(a, b)
			if got != tt.equal {
				t.Fatalf("strategiesEqual = %v, want %v", got, tt.equal)
			}
		})
	}
}

func TestFailureStage(t *testing.T) {
	tests := []struct {
		msg  string
		want stage
	}{
		{"repo: not found", stageRepo},
		{"clone failed", stageRepo},
		{"authenticated repo", stageRepo},
		{"repo invalid or private", stageRepo},
		{"inference: no match", stageInference},
		{"getting strategy: timeout", stageInference},
		{"build: failed", stageBuild},
		{"build: internal error", stageBuild},
		{"compare: content mismatch", stageCompare},
		{"something else entirely", stageUnknown},
		{"", stageUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			got := failureStage(tt.msg)
			if got != tt.want {
				t.Fatalf("failureStage(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestPipelineMilestones(t *testing.T) {
	// 10 total, 5 success, stages: 1 unknown, 1 repo, 1 inference, 1 build, 1 compare
	sc := StageCounts{1, 1, 1, 1, 1}
	repo, infer, build, repro := pipelineMilestones(10, 5, sc)
	// repoResolves = 10 - 1(repo) - 1(unknown) = 8
	if repo != 8 {
		t.Fatalf("repoResolves = %d, want 8", repo)
	}
	// inferenceSucceeds = 8 - 1(inference) = 7
	if infer != 7 {
		t.Fatalf("inferenceSucceeds = %d, want 7", infer)
	}
	// buildSucceeds = 7 - 1(build) = 6
	if build != 6 {
		t.Fatalf("buildSucceeds = %d, want 6", build)
	}
	// reproduces = success = 5
	if repro != 5 {
		t.Fatalf("reproduces = %d, want 5", repro)
	}
}

// buildTestDiffs creates a set of TargetDiffs covering all change types for rendering tests.
func buildTestDiffs() (DiffSummary, []TargetDiff) {
	stratA := strategyOneOf("https://github.com/foo/bar", "v1.0", "8.0.0")
	stratB := strategyOneOf("https://github.com/foo/bar", "v1.0", "9.0.0")
	rA := makeRebuild("npm", "a", "1.0", "", true, "", nil)
	rB := makeRebuild("npm", "a", "1.0", "", false, "build: failed", nil)
	iA := makeRebuild("npm", "b", "1.0", "", false, "repo: not found", nil)
	iB := makeRebuild("npm", "b", "1.0", "", true, "", nil)
	ceA := makeRebuild("npm", "c", "1.0", "", false, "error A", nil)
	ceB := makeRebuild("npm", "c", "1.0", "", false, "error B", nil)
	pA := makeRebuild("npm", "d", "1.0", "", false, "build: failed", nil)
	pB := makeRebuild("npm", "d", "1.0", "", false, "compare: content mismatch", nil)
	csA := makeRebuild("npm", "e", "1.0", "", true, "", stratA)
	csB := makeRebuild("npm", "e", "1.0", "", true, "", stratB)
	oaA := makeRebuild("npm", "f", "1.0", "", false, "inference: no match", nil)
	obB := makeRebuild("npm", "g", "1.0", "", true, "", nil)

	diffs := []TargetDiff{
		{Type: Regression, ID: rA.ID(), A: &rA, B: &rB},
		{Type: Improvement, ID: iA.ID(), A: &iA, B: &iB},
		{Type: ChangedError, ID: ceA.ID(), A: &ceA, B: &ceB},
		{Type: Progress, ID: pA.ID(), A: &pA, B: &pB},
		{Type: ChangedStrategy, ID: csA.ID(), A: &csA, B: &csB, StratDiff: "-npm_version: 8.0.0\n+npm_version: 9.0.0\n"},
		{Type: OnlyInA, ID: oaA.ID(), A: &oaA},
		{Type: OnlyInB, ID: obB.ID(), B: &obB},
	}
	summary := DiffSummary{
		RunA: "run1", RunB: "run2",
		TotalA: 10, TotalB: 10,
		SuccessA: 7, SuccessB: 8,
		StageCountsA: StageCounts{0, 0, 1, 1, 1},
		StageCountsB: StageCounts{0, 0, 0, 1, 1},
		Regressions:  1, Improvements: 1,
		ChangedErrs: 1, Progress: 1,
		ChangedStrats: 1,
		OnlyInA:       1, OnlyInB: 1, Unchanged: 0,
	}
	return summary, diffs
}

func TestRender(t *testing.T) {
	summary, diffs := buildTestDiffs()

	t.Run("summary", func(t *testing.T) {
		var buf bytes.Buffer
		renderSummary(&buf, summary)
		want := `Run A: run1 (10 total, 7 success, 70.0%)
Run B: run2 (10 total, 8 success, 80.0%)

Pipeline:
  Repo resolves:           10 (100.0%) ->   10 (100.0%)  (+0.0pp)
  Inference succeeds:       9 ( 90.0%) ->   10 (100.0%)  (+10.0pp)
  Build succeeds:           8 ( 80.0%) ->    9 ( 90.0%)  (+10.0pp)
  Reproduces:               7 ( 70.0%) ->    8 ( 80.0%)  (+10.0pp)

  Regressions:        1
  Improvements:       1
  Changed errors:     1
  Progress:           1
  Changed strategies: 1
  Only in A:          1
  Only in B:          1
  Unchanged:          0
`
		if got := buf.String(); got != want {
			t.Fatalf("renderSummary mismatch.\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("detail", func(t *testing.T) {
		var buf bytes.Buffer
		renderDetail(&buf, diffs)
		want := `=== Regressions (1) ===
  npm!a!1.0!
    A: success
    B: build: failed
=== Improvements (1) ===
  npm!b!1.0!
    A: repo: not found
    B: success
=== Changed Errors (1) ===
  npm!c!1.0!
    A: error A
    B: error B
=== Progress (1) ===
  npm!d!1.0!
    A: build: failed [build]
    B: compare: content mismatch [compare]
=== Changed Strategies (1) ===
  npm!e!1.0!
    -npm_version: 8.0.0
    +npm_version: 9.0.0
=== Only in A (1) ===
  npm!f!1.0!
    A: inference: no match
=== Only in B (1) ===
  npm!g!1.0!
    B: success
`
		if got := buf.String(); got != want {
			t.Fatalf("renderDetail mismatch.\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("csv", func(t *testing.T) {
		var buf bytes.Buffer
		renderCSV(&buf, diffs)
		want := `id,change_type,message_a,message_b,success_a,success_b
npm!a!1.0!,regression,,build: failed,true,false
npm!b!1.0!,improvement,repo: not found,,false,true
npm!c!1.0!,changed-error,error A,error B,false,false
npm!d!1.0!,progress,build: failed,compare: content mismatch,false,false
npm!e!1.0!,changed-strategy,,,true,true
npm!f!1.0!,only-in-a,inference: no match,,false,
npm!g!1.0!,only-in-b,,,,true
`
		if got := buf.String(); got != want {
			t.Fatalf("renderCSV mismatch.\ngot:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestApplyFilter(t *testing.T) {
	_, diffs := buildTestDiffs()

	tests := []struct {
		name      string
		filter    string
		wantCount int
		wantType  ChangeType // checked when wantCount == 1
	}{
		{"regressions", "regressions", 1, Regression},
		{"improvements", "improvements", 1, Improvement},
		{"changed-errors", "changed-errors", 1, ChangedError},
		{"progress", "progress", 1, Progress},
		{"changed-strategies", "changed-strategies", 1, ChangedStrategy},
		{"only-a", "only-a", 1, OnlyInA},
		{"only-b", "only-b", 1, OnlyInB},
		{"all", "all", len(diffs), 0},
		{"empty string", "", len(diffs), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := applyFilter(diffs, tt.filter)
			if len(filtered) != tt.wantCount {
				t.Fatalf("expected %d results, got %d", tt.wantCount, len(filtered))
			}
			if tt.wantCount == 1 && filtered[0].Type != tt.wantType {
				t.Fatalf("expected %v, got %v", tt.wantType, filtered[0].Type)
			}
		})
	}
}
