// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffruns

import (
	"sort"
	"strings"

	"github.com/google/oss-rebuild/internal/gitdiff"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"gopkg.in/yaml.v3"
)

// ChangeType categorizes the difference between two rebuild results.
type ChangeType int

const (
	Regression      ChangeType = iota // success -> failure
	Improvement                       // failure -> success
	ChangedError                      // failure -> failure, different message
	Progress                          // failure -> failure, moved to later pipeline stage
	ChangedStrategy                   // same outcome, different strategy
	OnlyInA                           // target only in baseline run
	OnlyInB                           // target only in candidate run
	Unchanged                         // identical outcome
)

func (c ChangeType) String() string {
	switch c {
	case Regression:
		return "regression"
	case Improvement:
		return "improvement"
	case ChangedError:
		return "changed-error"
	case Progress:
		return "progress"
	case ChangedStrategy:
		return "changed-strategy"
	case OnlyInA:
		return "only-in-a"
	case OnlyInB:
		return "only-in-b"
	case Unchanged:
		return "unchanged"
	default:
		return "unknown"
	}
}

// TargetDiff describes the difference for a single rebuild target between two runs.
type TargetDiff struct {
	Type      ChangeType
	ID        string          // Rebuild.ID() = ecosystem!package!version!artifact
	A         *rundex.Rebuild // nil if OnlyInB
	B         *rundex.Rebuild // nil if OnlyInA
	StratDiff string          // unified diff of YAML strategies (populated for ChangedStrategy)
}

// stage represents an ordered pipeline stage for failure classification.
type stage int

const (
	stageUnknown   stage = 0
	stageRepo      stage = 1 // repo:*, clone failed, authenticated repo, repo invalid or private
	stageInference stage = 2 // inference:*, getting strategy:*
	stageBuild     stage = 3 // build: failed, build: internal error
	stageCompare   stage = 4 // compare: content mismatch
)

func (s stage) String() string {
	switch s {
	case stageRepo:
		return "repo"
	case stageInference:
		return "inference"
	case stageBuild:
		return "build"
	case stageCompare:
		return "compare"
	default:
		return "unknown"
	}
}

// failureStage maps a cleaned error message to its pipeline stage.
func failureStage(msg string) stage {
	switch {
	case strings.HasPrefix(msg, "repo:"),
		strings.HasPrefix(msg, "clone failed"),
		strings.HasPrefix(msg, "authenticated repo"),
		strings.HasPrefix(msg, "repo invalid or private"):
		return stageRepo
	case strings.HasPrefix(msg, "inference:"),
		strings.HasPrefix(msg, "getting strategy:"):
		return stageInference
	case strings.HasPrefix(msg, "build: failed"),
		strings.HasPrefix(msg, "build: internal error"):
		return stageBuild
	case strings.HasPrefix(msg, "compare: content mismatch"):
		return stageCompare
	default:
		return stageUnknown
	}
}

// StageCounts holds the number of rebuilds that failed at each pipeline stage.
type StageCounts [5]int // indexed by stage constants

// DiffSummary aggregates counts for each change type.
type DiffSummary struct {
	RunA, RunB                  string
	TotalA, TotalB              int
	SuccessA, SuccessB          int
	StageCountsA, StageCountsB  StageCounts
	Regressions, Improvements   int
	ChangedErrs, Progress       int
	ChangedStrats               int
	OnlyInA, OnlyInB, Unchanged int
}

// DiffResult holds the full diff between two runs.
type DiffResult struct {
	Summary DiffSummary
	Diffs   []TargetDiff // sorted by ID
}

// strategiesEqual returns true if two strategies marshal to the same YAML.
func strategiesEqual(a, b rundex.Rebuild) bool {
	yamlA, errA := yaml.Marshal(a.Strategy)
	yamlB, errB := yaml.Marshal(b.Strategy)
	if errA != nil || errB != nil {
		return false
	}
	return string(yamlA) == string(yamlB)
}

// strategyDiff computes a unified diff between two strategies' YAML representations.
func strategyDiff(a, b rundex.Rebuild) string {
	yamlA, errA := yaml.Marshal(a.Strategy)
	yamlB, errB := yaml.Marshal(b.Strategy)
	if errA != nil || errB != nil {
		return ""
	}
	diff, err := gitdiff.Strings(string(yamlA), string(yamlB))
	if err != nil {
		return ""
	}
	return diff
}

// ComputeDiff compares two sets of rebuild results and classifies each target.
func ComputeDiff(runA, runB string, rebuildsA, rebuildsB []rundex.Rebuild) *DiffResult {
	mapA := make(map[string]rundex.Rebuild, len(rebuildsA))
	for _, r := range rebuildsA {
		mapA[r.ID()] = r
	}
	mapB := make(map[string]rundex.Rebuild, len(rebuildsB))
	for _, r := range rebuildsB {
		mapB[r.ID()] = r
	}
	// Union all keys.
	keySet := make(map[string]bool)
	for k := range mapA {
		keySet[k] = true
	}
	for k := range mapB {
		keySet[k] = true
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Construct result
	result := &DiffResult{
		Summary: DiffSummary{
			RunA:   runA,
			RunB:   runB,
			TotalA: len(rebuildsA),
			TotalB: len(rebuildsB),
		},
	}
	for _, r := range rebuildsA {
		if r.Success {
			result.Summary.SuccessA++
		} else {
			result.Summary.StageCountsA[failureStage(r.Message)]++
		}
	}
	for _, r := range rebuildsB {
		if r.Success {
			result.Summary.SuccessB++
		} else {
			result.Summary.StageCountsB[failureStage(r.Message)]++
		}
	}
	for _, key := range keys {
		a, inA := mapA[key]
		b, inB := mapB[key]
		var td TargetDiff
		td.ID = key
		switch {
		case inA && !inB:
			td.Type = OnlyInA
			td.A = &a
			result.Summary.OnlyInA++
		case !inA && inB:
			td.Type = OnlyInB
			td.B = &b
			result.Summary.OnlyInB++
		case a.Success && !b.Success:
			td.Type = Regression
			td.A = &a
			td.B = &b
			result.Summary.Regressions++
		case !a.Success && b.Success:
			td.Type = Improvement
			td.A = &a
			td.B = &b
			result.Summary.Improvements++
		case !a.Success && !b.Success && a.Message != b.Message:
			stageA, stageB := failureStage(a.Message), failureStage(b.Message)
			if stageA != stageUnknown && stageB != stageUnknown && stageB > stageA {
				td.Type = Progress
				result.Summary.Progress++
			} else {
				td.Type = ChangedError
				result.Summary.ChangedErrs++
			}
			td.A = &a
			td.B = &b
		case !strategiesEqual(a, b):
			td.Type = ChangedStrategy
			td.A = &a
			td.B = &b
			td.StratDiff = strategyDiff(a, b)
			result.Summary.ChangedStrats++
		default:
			td.Type = Unchanged
			td.A = &a
			td.B = &b
			result.Summary.Unchanged++
		}
		result.Diffs = append(result.Diffs, td)
	}
	return result
}
