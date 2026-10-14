// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/google/oss-rebuild/internal/docdb"
	"github.com/google/oss-rebuild/internal/iterx"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/internal/sqlitex"
	"github.com/google/oss-rebuild/internal/versionx"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
	"github.com/google/oss-rebuild/pkg/scheduler"
	"github.com/ncruces/go-sqlite3"
	"github.com/pkg/errors"
)

// Object is the destination object name of the snapshot database, stored
// gzip-compressed as its name says, and DeltaPrefix the prefix of its
// incremental segments. Both carry the schema version, so each version bump
// requires an artifact cutover. Within an era, the object name is stable
// with each rollup replacing it wholesale and the object store's versioning
// (e.g. GCS generations) responsible for providing historical entries.
var (
	Object      = fmt.Sprintf("rebuild-v%d.db.gz", SchemaVersion)
	DeltaPrefix = fmt.Sprintf("deltas-v%d", SchemaVersion)
)

// OpenCache serves a cached local copy of the snapshot database published
// under dest, refreshed in the background every interval until closed. It
// binds the docdb cache to this schema era: the versioned object and delta
// names, the table registry, the highest version this binary understands,
// and the meta row that carries the base watermark.
func OpenCache(ctx context.Context, dest billy.Filesystem, interval time.Duration) (*docdb.Cache, error) {
	watermark := func(db *sqlite3.Conn) (time.Time, error) {
		m, err := ReadMeta(db)
		return m.Watermark, err
	}
	return docdb.OpenCache(ctx, docdb.Upstream{
		FS: dest, Object: Object, Deltas: DeltaPrefix, Defs: Tables(), Schema: SchemaVersion, Watermark: watermark,
	}, interval)
}

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

// docsOf marshals source documents already in hand for a doc table.
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

// docSeq marshals a stream of source records into documents as they pass,
// so a doc table's rows never sit in memory together.
func docSeq[T any](xs iter.Seq2[T, error]) iter.Seq2[json.RawMessage, error] {
	return func(yield func(json.RawMessage, error) bool) {
		for x, err := range xs {
			if err != nil {
				yield(nil, err)
				return
			}
			b, err := json.Marshal(x)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(b, nil) {
				return
			}
		}
	}
}

// docProducers maps each doc table to the stream that fills it. A producer
// runs when fillSnapshotDB reaches its table, in registry order, so a later
// table's producer may depend on state an earlier stream left behind.
type docProducers map[string]func() iter.Seq2[json.RawMessage, error]

// Rollup streams src into the registry's doc tables, materializes the
// derived tables from them, and publishes the result as a single SQLite
// database under dest's versioned object. Each collection flows from the
// scan into its table without being held, so the run's memory is bounded by
// the tracked-package set rather than by history. The database's meta
// watermark is the scan start: every source write before it is captured,
// so incremental replay resumes there. The upload is one object write, so a
// partial failure never publishes an incomplete snapshot. It is a
// self-contained, idempotent per-invocation rollup.
func Rollup(ctx context.Context, src Source, dest billy.Filesystem, opts Options) (*RollupResult, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	fold := newSignalFold()
	producers := docProducers{
		TableAttempts: func() iter.Seq2[json.RawMessage, error] {
			return docSeq(fold.tapAttempts(src.Attempts(ctx, FullScan)))
		},
		TableRuns:            func() iter.Seq2[json.RawMessage, error] { return docSeq(src.Runs(ctx, FullScan)) },
		TableAgentSessions:   func() iter.Seq2[json.RawMessage, error] { return docSeq(src.Sessions(ctx, FullScan)) },
		TableAgentIterations: func() iter.Seq2[json.RawMessage, error] { return docSeq(src.Iterations(ctx, FullScan)) },
		TableScratchVMs:      func() iter.Seq2[json.RawMessage, error] { return docSeq(src.Scratches(ctx, FullScan)) },
		TableScratchExecs:    func() iter.Seq2[json.RawMessage, error] { return docSeq(src.Execs(ctx, FullScan)) },
		TableRepoMetrics:     func() iter.Seq2[json.RawMessage, error] { return docSeq(src.RepoMetrics(ctx, FullScan)) },
		TableCampaigns: func() iter.Seq2[json.RawMessage, error] {
			return docSeq(fold.tapCampaigns(src.Campaigns(ctx, FullScan)))
		},
		TablePackageSignals: func() iter.Seq2[json.RawMessage, error] {
			rows, builtAt, err := src.Signals(ctx)
			if err != nil {
				return iterx.Error[json.RawMessage](errors.Wrap(err, "reading priority signals"))
			}
			return docSeq(fold.prune(rows, builtAt))
		},
		TableSignalUniverse: func() iter.Seq2[json.RawMessage, error] { return docSeq(fold.universeRows()) },
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
	counts, err := buildSnapshotDB(dbPath, producers, meta)
	if err != nil {
		return nil, err
	}
	if err := sqlitex.Publish(dest, Object, dbPath); err != nil {
		return nil, errors.Wrap(err, "uploading snapshot")
	}
	return &RollupResult{Meta: meta, RowCounts: counts}, nil
}

// SignalUniverse is one ecosystem's package count and summed score across the
// whole signals export (the top --top packages by dependents). Coverage shares
// divide by it. It changes only when a larger export is published.
type SignalUniverse struct {
	Ecosystem      string
	Packages       int
	ScoreMass      float64
	SidecarBuiltAt time.Time
}

type trackedKey struct{ eco, pkg string }

// signalFold carries state from earlier streams to the signal tables: the
// tracked packages, recorded as attempts and campaigns stream past, and
// per-ecosystem totals of every signal row before pruning. Each step errors
// unless the streams it needs were fully read first.
type signalFold struct {
	tracked  map[trackedKey]bool
	scanned  map[string]bool
	byEco    map[string]int
	universe []SignalUniverse
}

func newSignalFold() *signalFold {
	return &signalFold{tracked: map[trackedKey]bool{}, scanned: map[string]bool{}, byEco: map[string]int{}}
}

func (f *signalFold) tapAttempts(xs iter.Seq2[schema.RebuildAttempt, error]) iter.Seq2[schema.RebuildAttempt, error] {
	return func(yield func(schema.RebuildAttempt, error) bool) {
		for a, err := range xs {
			if err == nil {
				f.tracked[trackedKey{a.Ecosystem, a.Package}] = true
			}
			if !yield(a, err) || err != nil {
				return
			}
		}
		f.scanned[TableAttempts] = true
	}
}

func (f *signalFold) tapCampaigns(xs iter.Seq2[scheduler.Campaign, error]) iter.Seq2[scheduler.Campaign, error] {
	return func(yield func(scheduler.Campaign, error) bool) {
		for c, err := range xs {
			if err == nil {
				f.tracked[trackedKey{c.Ecosystem, c.Package}] = true
			}
			if !yield(c, err) || err != nil {
				return
			}
		}
		f.scanned[TableCampaigns] = true
	}
}

// prune folds every signal row into the universe totals and yields only the
// tracked ones: those with an attempt or a campaign. The signal export holds
// every ranked package, tracked or not, so without this package_signals would
// scale with the export rather than with coverage. Presence in this database
// is the rollup's only notion of a tracked package.
// TODO: Reconcile with the campaign and enqueue machinery once a single
// tracked-set authority exists.
func (f *signalFold) prune(rows iter.Seq2[signals.PackageSignal, error], builtAt time.Time) iter.Seq2[signals.PackageSignal, error] {
	return func(yield func(signals.PackageSignal, error) bool) {
		if !f.scanned[TableAttempts] || !f.scanned[TableCampaigns] {
			yield(signals.PackageSignal{}, errors.New("package_signals must fill after attempts and campaigns"))
			return
		}
		for s, err := range rows {
			if err != nil {
				yield(s, err)
				return
			}
			i, ok := f.byEco[s.Ecosystem]
			if !ok {
				i = len(f.universe)
				f.byEco[s.Ecosystem] = i
				f.universe = append(f.universe, SignalUniverse{Ecosystem: s.Ecosystem, SidecarBuiltAt: builtAt})
			}
			f.universe[i].Packages++
			f.universe[i].ScoreMass += s.Score
			if !f.tracked[trackedKey{s.Ecosystem, s.Package}] {
				continue
			}
			if !yield(s, nil) {
				return
			}
		}
		f.scanned[TablePackageSignals] = true
	}
}

// universeRows yields the folded totals, complete once prune has drained.
func (f *signalFold) universeRows() iter.Seq2[SignalUniverse, error] {
	if !f.scanned[TablePackageSignals] {
		return iterx.Error[SignalUniverse](errors.New("signal_universe must fill after package_signals"))
	}
	return iterx.FromSlice(f.universe)
}

// buildSnapshotDB writes every registry table plus the database meta to a
// new database at path, returning per-table row counts. Each doc table must
// have a producer, so the registry and the scan cannot drift apart
// silently. Derived tables materialize from their queries in registry
// order.
func buildSnapshotDB(path string, producers docProducers, meta Meta) (map[string]int, error) {
	db, err := sqlite3.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "creating database")
	}
	if err := registerCollations(db); err != nil {
		db.Close()
		return nil, err
	}
	counts, err := fillSnapshotDB(db, producers, meta)
	if err != nil {
		db.Close()
		return nil, err
	}
	return counts, errors.Wrap(db.Close(), "closing database")
}

// registerCollations registers the version_approx_compare collation the
// derived queries use to order version strings. Only build connections need
// it: derived tables are materialized, so readers never re-run the queries.
func registerCollations(db *sqlite3.Conn) error {
	err := db.CreateCollation("version_approx_compare", func(a, b []byte) int {
		return versionx.ApproxCompare(string(a), string(b))
	})
	return errors.Wrap(err, "registering version_approx_compare collation")
}

func fillSnapshotDB(db *sqlite3.Conn, producers docProducers, meta Meta) (map[string]int, error) {
	counts := make(map[string]int, len(Tables()))
	docTableCount := 0
	for _, td := range Tables() {
		if td.Query != "" {
			continue
		}
		docTableCount++
		produce, ok := producers[td.Name]
		if !ok {
			return nil, errors.Errorf("no producer for doc table %s", td.Name)
		}
		n, err := docdb.StoreDocSeq(db, td, produce())
		if err != nil {
			return nil, errors.Wrapf(err, "storing %s", td.Name)
		}
		counts[td.Name] = n
	}
	if docTableCount != len(producers) {
		return nil, errors.Errorf("producers for %d tables, registry declares %d doc tables", len(producers), docTableCount)
	}
	derived, err := refreshDerived(db)
	if err != nil {
		return nil, err
	}
	maps.Copy(counts, derived)
	if err := sqlitex.SetVersion(db, SchemaVersion); err != nil {
		return nil, err
	}
	return counts, WriteMeta(db, meta)
}

// refreshDerived drops and rematerializes every derived table from the doc
// rows currently in db, in registry order so a derived table may build on
// an earlier one. Idempotent, so it serves both the one-shot snapshot build
// and in-place refresh of a locally written database.
func refreshDerived(db *sqlite3.Conn) (map[string]int, error) {
	counts := make(map[string]int)
	for _, td := range Tables() {
		if td.Query == "" {
			continue
		}
		if err := db.Exec("DROP TABLE IF EXISTS " + td.Name); err != nil {
			return nil, errors.Wrapf(err, "dropping %s", td.Name)
		}
		n, err := docdb.StoreQuery(db, td)
		if err != nil {
			return nil, errors.Wrapf(err, "materializing %s", td.Name)
		}
		counts[td.Name] = n
	}
	return counts, nil
}
