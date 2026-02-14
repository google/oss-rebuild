// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package diffruns

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"path/filepath"

	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/google/oss-rebuild/tools/ctl/rundex"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the diff-runs command.
type Config struct {
	Project string
	RunA    string
	RunB    string
	Bench   string
	Clean   bool
	Format  string
	Filter  string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.RunA == "" {
		return errors.New("run-a is required")
	}
	if c.RunB == "" {
		return errors.New("run-b is required")
	}
	if c.RunA == c.RunB {
		return errors.New("run-a and run-b must be different")
	}
	switch c.Format {
	case "", "summary", "detail", "csv":
	default:
		return errors.Errorf("invalid format: %s. Expected one of 'summary', 'detail', or 'csv'", c.Format)
	}
	switch c.Filter {
	case "", "all", "regressions", "improvements", "changed-errors", "progress", "changed-strategies", "only-a", "only-b":
	default:
		return errors.Errorf("invalid filter: %s. Expected one of 'all', 'regressions', 'improvements', 'changed-errors', 'progress', 'changed-strategies', 'only-a', or 'only-b'", c.Filter)
	}
	return nil
}

// Deps holds dependencies for the command.
type Deps struct {
	IO cli.IO
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps initializes Deps.
func InitDeps(context.Context) (*Deps, error) {
	return &Deps{}, nil
}

func fetchRebuilds(ctx context.Context, dex rundex.Reader, runID, bench string, clean bool) ([]rundex.Rebuild, error) {
	req := &rundex.FetchRebuildRequest{
		Runs:             []string{runID},
		LatestPerPackage: true,
		Opts: rundex.FetchRebuildOpts{
			Clean: clean,
		},
	}
	if bench != "" {
		log.Printf("Extracting benchmark %s...\n", filepath.Base(bench))
		set, err := benchmark.ReadBenchmark(bench)
		if err != nil {
			return nil, errors.Wrap(err, "reading benchmark file")
		}
		req.Bench = &set
		log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
	}
	return dex.FetchRebuilds(ctx, req)
}

// Handler contains the business logic for the diff-runs command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	var dex rundex.Reader
	var err error
	if cfg.Project == "" {
		log.Printf("No project specified, using local results from %s", localfiles.Rundex())
		dex = rundex.NewFilesystemClient(localfiles.Rundex())
	} else {
		dex, err = rundex.NewFirestore(ctx, cfg.Project)
		if err != nil {
			return nil, err
		}
	}
	var rebuildsA, rebuildsB []rundex.Rebuild
	{
		log.Printf("Fetching results for run A: %s", cfg.RunA)
		rebuildsA, err := fetchRebuilds(ctx, dex, cfg.RunA, cfg.Bench, cfg.Clean)
		if err != nil {
			return nil, errors.Wrap(err, "fetching run A")
		}
		log.Printf("Fetched %d rebuilds for run A", len(rebuildsA))
		if len(rebuildsA) == 0 {
			return nil, errors.Errorf("no rebuilds found for run A: %s", cfg.RunA)
		}
	}
	{
		log.Printf("Fetching results for run B: %s", cfg.RunB)
		rebuildsB, err := fetchRebuilds(ctx, dex, cfg.RunB, cfg.Bench, cfg.Clean)
		if err != nil {
			return nil, errors.Wrap(err, "fetching run B")
		}
		log.Printf("Fetched %d rebuilds for run B", len(rebuildsB))
		if len(rebuildsB) == 0 {
			return nil, errors.Errorf("no rebuilds found for run B: %s", cfg.RunB)
		}
	}
	result := ComputeDiff(cfg.RunA, cfg.RunB, rebuildsA, rebuildsB)
	diffs := applyFilter(result.Diffs, cfg.Filter)
	switch cfg.Format {
	case "", "summary":
		renderSummary(deps.IO.Out, result.Summary)
	case "detail":
		renderSummary(deps.IO.Out, result.Summary)
		fmt.Fprintln(deps.IO.Out)
		renderDetail(deps.IO.Out, diffs)
	case "csv":
		renderCSV(deps.IO.Out, diffs)
	}
	return &act.NoOutput{}, nil
}

func applyFilter(diffs []TargetDiff, filter string) []TargetDiff {
	if filter == "" || filter == "all" {
		return diffs
	}
	var ct ChangeType
	switch filter {
	case "regressions":
		ct = Regression
	case "improvements":
		ct = Improvement
	case "changed-errors":
		ct = ChangedError
	case "progress":
		ct = Progress
	case "changed-strategies":
		ct = ChangedStrategy
	case "only-a":
		ct = OnlyInA
	case "only-b":
		ct = OnlyInB
	default:
		return diffs
	}
	var filtered []TargetDiff
	for _, d := range diffs {
		if d.Type == ct {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

func renderSummary(w io.Writer, s DiffSummary) {
	fmt.Fprintf(w, "Run A: %s (%d total, %d success, %.1f%%)\n", s.RunA, s.TotalA, s.SuccessA, pct(s.SuccessA, s.TotalA))
	fmt.Fprintf(w, "Run B: %s (%d total, %d success, %.1f%%)\n", s.RunB, s.TotalB, s.SuccessB, pct(s.SuccessB, s.TotalB))
	fmt.Fprintln(w)
	renderPipeline(w, s)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  Regressions:        %d\n", s.Regressions)
	fmt.Fprintf(w, "  Improvements:       %d\n", s.Improvements)
	fmt.Fprintf(w, "  Changed errors:     %d\n", s.ChangedErrs)
	fmt.Fprintf(w, "  Progress:           %d\n", s.Progress)
	fmt.Fprintf(w, "  Changed strategies: %d\n", s.ChangedStrats)
	fmt.Fprintf(w, "  Only in A:          %d\n", s.OnlyInA)
	fmt.Fprintf(w, "  Only in B:          %d\n", s.OnlyInB)
	fmt.Fprintf(w, "  Unchanged:          %d\n", s.Unchanged)
}

// pipelineMilestones computes cumulative "reaches at least stage X" counts from stage counts and success.
func pipelineMilestones(total, success int, sc StageCounts) (repoResolves, inferenceSucceeds, buildSucceeds, reproduces int) {
	repoResolves = total - sc[stageRepo] - sc[stageUnknown]
	inferenceSucceeds = repoResolves - sc[stageInference]
	buildSucceeds = inferenceSucceeds - sc[stageBuild]
	reproduces = success
	return
}

func renderPipeline(w io.Writer, s DiffSummary) {
	repoA, inferA, buildA, reproA := pipelineMilestones(s.TotalA, s.SuccessA, s.StageCountsA)
	repoB, inferB, buildB, reproB := pipelineMilestones(s.TotalB, s.SuccessB, s.StageCountsB)
	fmt.Fprintln(w, "Pipeline:")
	milestones := []struct {
		label  string
		countA int
		countB int
	}{
		{"Repo resolves", repoA, repoB},
		{"Inference succeeds", inferA, inferB},
		{"Build succeeds", buildA, buildB},
		{"Reproduces", reproA, reproB},
	}
	for _, m := range milestones {
		pctA := pct(m.countA, s.TotalA)
		pctB := pct(m.countB, s.TotalB)
		delta := pctB - pctA
		sign := "+"
		if delta < 0 {
			sign = ""
		}
		fmt.Fprintf(w, "  %-22s %4d (%5.1f%%) -> %4d (%5.1f%%)  (%s%.1fpp)\n",
			m.label+":", m.countA, pctA, m.countB, pctB, sign, delta)
	}
}

func renderDetail(w io.Writer, diffs []TargetDiff) {
	sections := []struct {
		ct    ChangeType
		label string
	}{
		{Regression, "Regressions"},
		{Improvement, "Improvements"},
		{ChangedError, "Changed Errors"},
		{Progress, "Progress"},
		{ChangedStrategy, "Changed Strategies"},
		{OnlyInA, "Only in A"},
		{OnlyInB, "Only in B"},
	}
	for _, sec := range sections {
		var matching []TargetDiff
		for _, d := range diffs {
			if d.Type == sec.ct {
				matching = append(matching, d)
			}
		}
		if len(matching) == 0 {
			continue
		}
		fmt.Fprintf(w, "=== %s (%d) ===\n", sec.label, len(matching))
		for _, d := range matching {
			fmt.Fprintf(w, "  %s\n", d.ID)
			switch d.Type {
			case Regression:
				fmt.Fprintf(w, "    A: success\n")
				fmt.Fprintf(w, "    B: %s\n", d.B.Message)
			case Improvement:
				fmt.Fprintf(w, "    A: %s\n", d.A.Message)
				fmt.Fprintf(w, "    B: success\n")
			case ChangedError:
				fmt.Fprintf(w, "    A: %s\n", d.A.Message)
				fmt.Fprintf(w, "    B: %s\n", d.B.Message)
			case Progress:
				fmt.Fprintf(w, "    A: %s [%s]\n", d.A.Message, failureStage(d.A.Message))
				fmt.Fprintf(w, "    B: %s [%s]\n", d.B.Message, failureStage(d.B.Message))
			case ChangedStrategy:
				if d.StratDiff != "" {
					fmt.Fprintf(w, "%s", indentLines(d.StratDiff, "    "))
				}
			case OnlyInA:
				if d.A.Success {
					fmt.Fprintf(w, "    A: success\n")
				} else {
					fmt.Fprintf(w, "    A: %s\n", d.A.Message)
				}
			case OnlyInB:
				if d.B.Success {
					fmt.Fprintf(w, "    B: success\n")
				} else {
					fmt.Fprintf(w, "    B: %s\n", d.B.Message)
				}
			}
		}
	}
}

func renderCSV(w io.Writer, diffs []TargetDiff) {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"id", "change_type", "message_a", "message_b", "success_a", "success_b"})
	for _, d := range diffs {
		var msgA, msgB, sucA, sucB string
		if d.A != nil {
			msgA = d.A.Message
			sucA = fmt.Sprintf("%t", d.A.Success)
		}
		if d.B != nil {
			msgB = d.B.Message
			sucB = fmt.Sprintf("%t", d.B.Success)
		}
		cw.Write([]string{d.ID, d.Type.String(), msgA, msgB, sucA, sucB})
	}
}

func indentLines(s, prefix string) string {
	var result string
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] != '\n' {
			j++
		}
		result += prefix + s[i:j] + "\n"
		if j < len(s) {
			j++ // skip newline
		}
		i = j
	}
	return result
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100.0 * float64(n) / float64(total)
}

// Command creates a new diff-runs command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "diff-runs --run-a <ID> --run-b <ID> [-project <ID>] [-bench <benchmark.json>] [-format summary|detail|csv] [-filter all|regressions|improvements|changed-errors|progress|changed-strategies|only-a|only-b]",
		Short: "Compare rebuild results between two runs",
		Args:  cobra.NoArgs,
		RunE: cli.RunE(
			&cfg,
			cli.SkipArgs[Config],
			InitDeps,
			Handler,
		),
	}
	cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
	return cmd
}

func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "the project from which to fetch the Firestore data")
	set.StringVar(&cfg.RunA, "run-a", "", "the baseline run ID")
	set.StringVar(&cfg.RunB, "run-b", "", "the candidate run ID")
	set.StringVar(&cfg.Bench, "bench", "", "a path to a benchmark file for filtering")
	set.BoolVar(&cfg.Clean, "clean", true, "whether to apply normalization heuristics to group similar verdicts")
	set.StringVar(&cfg.Format, "format", "", "format of the output (summary|detail|csv)")
	set.StringVar(&cfg.Filter, "filter", "", "filter results by change type (all|regressions|improvements|changed-errors|progress|changed-strategies|only-a|only-b)")
	return set
}
