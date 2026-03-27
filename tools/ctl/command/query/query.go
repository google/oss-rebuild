// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package query

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/tools/benchmark"
	"github.com/google/oss-rebuild/tools/ctl/assetlocator"
	"github.com/google/oss-rebuild/tools/ctl/localfiles"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the query command.
type Config struct {
	Project        string
	Run            string
	Bench          string
	LogsBucket     string
	MetadataBucket string
	DebugStorage   string
	query          string // populated from positional arg, not a flag
}

// Validate ensures the configuration is valid.
func (c Config) Validate() error {
	return nil
}

// Deps holds dependencies for the command.
type Deps struct {
	IO                 cli.IO
	FilesystemReaderFn func() rundex.Reader
	FirestoreReaderFn  func(ctx context.Context, project string) (rundex.Reader, error)
}

func (d *Deps) SetIO(cio cli.IO) { d.IO = cio }

// InitDeps initializes Deps.
func InitDeps(context.Context) (*Deps, error) {
	return &Deps{
		FilesystemReaderFn: func() rundex.Reader {
			return rundex.NewFilesystemClient(localfiles.Rundex())
		},
		FirestoreReaderFn: func(ctx context.Context, project string) (rundex.Reader, error) {
			return rundex.NewFirestore(ctx, project)
		},
	}, nil
}

// Handler contains the business logic for the query command.
func Handler(ctx context.Context, cfg Config, deps *Deps) (*act.NoOutput, error) {
	var runIDs []string
	if cfg.Run != "" {
		runIDs = strings.Split(cfg.Run, ",")
	}
	var bench *benchmark.PackageSet
	if cfg.Bench != "" {
		log.Printf("Extracting benchmark %s...\n", filepath.Base(cfg.Bench))
		set, err := benchmark.ReadBenchmark(cfg.Bench)
		if err != nil {
			return nil, errors.Wrap(err, "reading benchmark file")
		}
		bench = &set
		log.Printf("Loaded benchmark of %d artifacts...\n", set.Count)
	}
	var dex rundex.Reader
	var err error
	if cfg.Project == "" {
		dex = deps.FilesystemReaderFn()
	} else {
		dex, err = deps.FirestoreReaderFn(ctx, cfg.Project)
		if err != nil {
			return nil, errors.Wrap(err, "creating firestore client")
		}
	}
	// Open in-memory SQLite and register virtual tables.
	db, err := sqlite3.Open(":memory:")
	if err != nil {
		return nil, errors.Wrap(err, "opening in-memory database")
	}
	defer db.Close()
	db.SetInterrupt(ctx)
	if err := registerRunsVTab(db, ctx, dex); err != nil {
		return nil, errors.Wrap(err, "registering runs vtable")
	}
	if err := registerRebuildsVTab(db, ctx, dex, runIDs, bench); err != nil {
		return nil, errors.Wrap(err, "registering rebuilds vtable")
	}
	if cfg.LogsBucket != "" {
		assets := &assetlocator.MetaAssetStore{
			LogsBucket:     cfg.LogsBucket,
			MetadataBucket: cfg.MetadataBucket,
			DebugStorage:   cfg.DebugStorage,
		}
		if err := registerLogsVTab(db, ctx, assets); err != nil {
			return nil, errors.Wrap(err, "registering logs vtable")
		}
		if err := registerNetlogVTab(db, ctx, assets); err != nil {
			return nil, errors.Wrap(err, "registering netlog vtable")
		}
		sgc := newSGCache(ctx, assets)
		if err := registerSGActionsVTab(db, sgc); err != nil {
			return nil, errors.Wrap(err, "registering sg_actions vtable")
		}
		if err := registerSGIOVTab(db, sgc); err != nil {
			return nil, errors.Wrap(err, "registering sg_io vtable")
		}
		if err := registerSGResourcesVTab(db, sgc); err != nil {
			return nil, errors.Wrap(err, "registering sg_resources vtable")
		}
	}
	// Execute the query.
	stmt, _, err := db.Prepare(cfg.query)
	if err != nil {
		return nil, errors.Wrap(err, "preparing query")
	}
	defer stmt.Close()
	// Print column headers.
	ncols := stmt.ColumnCount()
	cols := make([]string, ncols)
	for i := range ncols {
		cols[i] = stmt.ColumnName(i)
	}
	tw := tabwriter.NewWriter(deps.IO.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	// Print rows.
	for stmt.Step() {
		parts := make([]string, ncols)
		for i := range ncols {
			parts[i] = stmt.ColumnText(i)
		}
		fmt.Fprintln(tw, strings.Join(parts, "\t"))
	}
	if err := stmt.Err(); err != nil {
		return nil, errors.Wrap(err, "executing query")
	}
	tw.Flush()
	return &act.NoOutput{}, nil
}

// Command creates a new query command instance.
func Command() *cobra.Command {
	cfg := Config{}
	cmd := &cobra.Command{
		Use:   "query [flags] <SQL>",
		Short: "Run SQL against run and rebuild metadata",
		Long: `Run SQL queries against run and rebuild metadata using read-only virtual tables.
Data is fetched on demand from Firestore/GCS/local storage.

Available tables:
  runs      (id, benchmark, benchmark_hash, type, created)
  rebuilds  (ecosystem, package, version, artifact, success, message,
             executor_version, run_id, build_id, started, ended,
             duration_s)
  logs      (ecosystem, package, version, artifact, run_id, content)
  netlog    (ecosystem, package, version, artifact, run_id,
             method, scheme, host, path, url, purl, time)
  sg_actions (ecosystem, package, version, artifact, run_id,
             action_id, parent_id, is_entry_point, command,
             executable, cwd, pid, start_time, end_time, duration_s,
             is_fork, exit_status)
  sg_io     (ecosystem, package, version, artifact, run_id,
             action_id, resource_digest, direction, io_type, time,
             total_size, bytes_used, resource_type, path, address)
  sg_resources (ecosystem, package, version, artifact, run_id,
             digest, type, path, file_type, file_digest, address, protocol)
             -- asset tables require --logs-bucket, --metadata-bucket,
                --debug-storage

Note: All virtual tables are read-only; write operations (INSERT, UPDATE, DELETE)
are not supported and will fail.

The --run flag provides default run scoping for the rebuilds table. It can
also be expressed as a WHERE clause: "WHERE run_id = '...'" which pushes
down to the storage backend.

Examples:
  query --project my-proj \
    "SELECT * FROM rebuilds WHERE run_id = '2026-03-11T06:18:13Z' LIMIT 10"

  query --project my-proj --run 2026-03-11T06:18:13Z \
    "SELECT ecosystem, count(*) FROM rebuilds GROUP BY ecosystem"

  query --project my-proj --run 2026-03-11T06:18:13Z \
    "SELECT r.id, count(*) FROM runs r JOIN rebuilds b ON r.id = b.run_id GROUP BY r.id"`,
		Args: cobra.ExactArgs(1),
		RunE: cli.RunE(
			&cfg,
			func(c *Config, args []string) error {
				c.query = args[0]
				return nil
			},
			InitDeps,
			Handler,
		),
	}
	cmd.Flags().AddGoFlagSet(flagSet(cmd.Name(), &cfg))
	return cmd
}

// flagSet returns the command-line flags for the Config struct.
func flagSet(name string, cfg *Config) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "the project from which to fetch the Firestore data")
	set.StringVar(&cfg.Run, "run", "", "the run(s) from which to fetch results (comma-separated)")
	set.StringVar(&cfg.Bench, "bench", "", "a path to a benchmark file for filtering")
	set.StringVar(&cfg.LogsBucket, "logs-bucket", "", "GCS bucket for build logs (enables logs table)")
	set.StringVar(&cfg.MetadataBucket, "metadata-bucket", "", "GCS bucket for rebuild metadata")
	set.StringVar(&cfg.DebugStorage, "debug-storage", "", "GCS bucket for debug artifacts (e.g. gs://bucket-name)")
	return set
}
