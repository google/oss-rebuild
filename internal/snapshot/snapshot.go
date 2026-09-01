// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/docdb"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// Object is the destination object name of the snapshot database, stored
// gzip-compressed as its name says. It carries the schema version, so each
// version bump requires an artifact cutover. Within an era, the name is
// stable with each rollup replaces the object wholesale with the object
// store's versioning (e.g. GCS generations) responsible for providing
// historical entries.
var Object = fmt.Sprintf("rebuild-v%d.db.gz", SchemaVersion)

// Options configures a snapshot run.
type Options struct {
	Project     string    // recorded in the database meta as the source project
	ToolVersion string    // recorded in the database meta (e.g. buildinfo.Version)
	Now         time.Time // overrides the snapshot timestamp. Zero means time.Now().UTC()
}

// RollupResult reports what a snapshot run wrote.
type RollupResult struct {
	Meta      Meta
	RowCounts map[string]int
}

// docsOf marshals source documents for a doc table.
func docsOf[T any](xs []T) []json.RawMessage {
	docs := make([]json.RawMessage, 0, len(xs))
	for _, x := range xs {
		if b, err := json.Marshal(x); err != nil {
			panic(err)
		} else {
			docs = append(docs, b)
		}
	}
	return docs
}

// Rollup scans src into the registry's doc tables, materializes the
// derived tables from them, and publishes the result as a single SQLite
// database under dest's versioned object. The database's meta watermark is
// the scan start: every source write before it is captured, so incremental
// replay resumes there. The upload is one object write, so a partial
// failure never publishes an incomplete snapshot. It is a self-contained,
// idempotent per-invocation rollup.
func Rollup(ctx context.Context, src Source, dest billy.Filesystem, opts Options) (*RollupResult, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	attempts, err := src.Attempts(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "scanning attempts")
	}
	runs, err := src.Runs(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "scanning runs")
	}
	sessions, err := src.Sessions(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "scanning agent sessions")
	}
	iterations, err := src.Iterations(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "scanning agent iterations")
	}
	scratches, err := src.Scratches(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "scanning scratch VMs")
	}
	execs, err := src.Execs(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "scanning scratch execs")
	}
	repoMetrics, err := src.RepoMetrics(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "scanning repo metrics")
	}
	docTables := map[string][]json.RawMessage{
		TableAttempts:        docsOf(attempts),
		TableRuns:            docsOf(runs),
		TableAgentSessions:   docsOf(sessions),
		TableAgentIterations: docsOf(iterations),
		TableScratchVMs:      docsOf(scratches),
		TableScratchExecs:    docsOf(execs),
		TableRepoMetrics:     docsOf(repoMetrics),
	}
	meta := Meta{
		BuiltAt:       now,
		Watermark:     now,
		SourceProject: opts.Project,
		ToolVersion:   opts.ToolVersion,
	}
	dir, err := os.MkdirTemp("", "snapshot-rollup-")
	if err != nil {
		return nil, errors.Wrap(err, "creating build directory")
	}
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "snapshot.db")
	counts, err := buildSnapshotDB(dbPath, docTables, meta)
	if err != nil {
		return nil, err
	}
	if err := sqlitex.Publish(dest, Object, dbPath); err != nil {
		return nil, errors.Wrap(err, "uploading snapshot")
	}
	return &RollupResult{Meta: meta, RowCounts: counts}, nil
}

// buildSnapshotDB writes every registry table plus the database meta to a
// new database at path, returning per-table row counts. Each doc table must
// have documents built for it, so the registry and the scan cannot drift
// apart silently. Derived tables materialize from their queries in registry
// order.
func buildSnapshotDB(path string, docTables map[string][]json.RawMessage, meta Meta) (map[string]int, error) {
	db, err := sqlite3.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "creating database")
	}
	counts, err := fillSnapshotDB(db, docTables, meta)
	if err != nil {
		db.Close()
		return nil, err
	}
	return counts, errors.Wrap(db.Close(), "closing database")
}

func fillSnapshotDB(db *sqlite3.Conn, docTables map[string][]json.RawMessage, meta Meta) (map[string]int, error) {
	counts := make(map[string]int, len(Tables()))
	docTableCount := 0
	for _, td := range Tables() {
		if td.Query != "" {
			n, err := docdb.StoreQuery(db, td)
			if err != nil {
				return nil, errors.Wrapf(err, "materializing %s", td.Name)
			}
			counts[td.Name] = n
			continue
		}
		docTableCount++
		docs, ok := docTables[td.Name]
		if !ok {
			return nil, errors.Errorf("no documents built for doc table %s", td.Name)
		}
		if err := docdb.StoreDocs(db, td, docs); err != nil {
			return nil, errors.Wrapf(err, "storing %s", td.Name)
		}
		counts[td.Name] = len(docs)
	}
	if docTableCount != len(docTables) {
		return nil, errors.Errorf("built documents for %d tables, registry declares %d doc tables", len(docTables), docTableCount)
	}
	if err := sqlitex.SetVersion(db, SchemaVersion); err != nil {
		return nil, err
	}
	return counts, WriteMeta(db, meta)
}
