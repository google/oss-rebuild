// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package snapshot implements the `ctl snapshot` command group: rollup
// scans a project's Firestore and writes the snapshot database to a
// file:// or gs:// destination, and delta writes one segment of recent
// writes beside it.
package snapshot

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/buildinfo"
	"github.com/google/oss-rebuild/internal/snapshot"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds configuration for `ctl snapshot rollup`.
type Config struct {
	Project   string
	Dest      string
	SignalsDB string
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.Dest == "" {
		return errors.New("dest is required")
	}
	return nil
}

// Deps holds dependencies for the command.
type Deps struct {
	IO cli.IO
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps initializes Deps.
func InitDeps(context.Context) (*Deps, error) { return &Deps{}, nil }

// RollupHandler scans Firestore and writes a snapshot to the destination.
func RollupHandler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	src, err := snapshot.NewFirestoreSource(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore source")
	}
	defer src.Close()
	resolve := billyx.NewResolver()
	if cfg.SignalsDB != "" {
		if src.SignalsDB, err = resolve.DirFS(ctx, cfg.SignalsDB); err != nil {
			return nil, err
		}
	}
	dest, err := resolve.DirFS(ctx, cfg.Dest)
	if err != nil {
		return nil, err
	}
	res, err := snapshot.Rollup(ctx, src, dest, snapshot.Options{Project: cfg.Project, ToolVersion: buildinfo.Version})
	if err != nil {
		return nil, errors.Wrap(err, "running rollup")
	}
	fmt.Fprintf(deps.IO.Out, "wrote %s (watermark %s)\n", snapshot.Object, res.Meta.Watermark.Format(time.RFC3339))
	printRowCounts(deps.IO.Out, res.RowCounts)
	return &act.NoOutput{}, nil
}

// printRowCounts lists per-table row counts in table-name order.
func printRowCounts(out io.Writer, counts map[string]int) {
	tables := make([]string, 0, len(counts))
	for t := range counts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		fmt.Fprintf(out, "  %-20s %d rows\n", t, counts[t])
	}
}

// DeltaHandler writes one delta segment to the destination.
func DeltaHandler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	src, err := snapshot.NewFirestoreSource(ctx, cfg.Project)
	if err != nil {
		return nil, errors.Wrap(err, "creating firestore source")
	}
	defer src.Close()
	dest, err := billyx.NewResolver().DirFS(ctx, cfg.Dest)
	if err != nil {
		return nil, err
	}
	res, err := snapshot.Delta(ctx, src, dest, snapshot.DeltaOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "running delta")
	}
	if res.Segment == "" {
		fmt.Fprintf(deps.IO.Out, "no changes since %s\n", res.Since.Format(time.RFC3339))
		return &act.NoOutput{}, nil
	}
	fmt.Fprintf(deps.IO.Out, "wrote %s (since %s)\n", res.Segment, res.Since.Format(time.RFC3339))
	printRowCounts(deps.IO.Out, res.RowCounts)
	return &act.NoOutput{}, nil
}

// deltaCommand creates the `snapshot delta` subcommand.
func deltaCommand() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "delta --project <ID> --dest <gs://...|file://...>",
		Short: "Write a delta segment of recent writes",
		Args:  cobra.NoArgs,
		RunE: cli.RunE(
			&cfg,
			cli.SkipArgs[Config],
			InitDeps,
			DeltaHandler,
		),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "the GCP project whose Firestore to scan")
	set.StringVar(&cfg.Dest, "dest", "", "segment destination URI (gs://bucket/prefix or file:///abs/path)")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

// rollupCommand creates the `snapshot rollup` subcommand.
func rollupCommand() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "rollup --project <ID> --dest <gs://...|file://...>",
		Short: "Scan Firestore and write the snapshot database",
		Args:  cobra.NoArgs,
		RunE: cli.RunE(
			&cfg,
			cli.SkipArgs[Config],
			InitDeps,
			RollupHandler,
		),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "the GCP project whose Firestore to scan")
	set.StringVar(&cfg.Dest, "dest", "", "snapshot destination URI (gs://bucket/prefix or file:///abs/path)")
	set.StringVar(&cfg.SignalsDB, "signals-db", "", "URI the signal database publishes under (gs:// or file://); empty skips signal ingest")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}

// Command creates the `snapshot` parent command.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Snapshot database rollups and delta segments",
	}
	cmd.AddCommand(rollupCommand())
	cmd.AddCommand(deltaCommand())
	return cmd
}
