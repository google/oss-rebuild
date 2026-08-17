// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package onboard

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/google/oss-rebuild/internal/billyx"
	"github.com/google/oss-rebuild/internal/jsonl"
	"github.com/google/oss-rebuild/internal/signals"
	"github.com/google/oss-rebuild/pkg/act"
	"github.com/google/oss-rebuild/pkg/act/cli"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type prevalenceConfig struct {
	Project     string
	Ecosystems  string
	Top         int // bounds the package rows kept per ecosystem
	TopVersions int // bounds the version rows kept per ecosystem
	Out         string
}

func (c prevalenceConfig) Validate() error {
	if c.Project == "" {
		return errors.New("project is required")
	}
	if c.Out == "" {
		return errors.New("out is required")
	}
	ecos := c.ecosystems()
	if len(ecos) == 0 {
		return errors.New("at least one ecosystem is required")
	}
	for _, name := range ecos {
		if _, ok := signals.EcosystemSystem[rebuild.Ecosystem(name)]; !ok {
			return errors.Errorf("ecosystem %q has no deps.dev dependency-graph coverage", name)
		}
	}
	return nil
}

func (c prevalenceConfig) ecosystems() []string {
	var out []string
	for name := range strings.SplitSeq(c.Ecosystems, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func prevalenceHandler(ctx context.Context, cfg prevalenceConfig, deps *Deps) (*act.NoOutput, error) {
	client, err := bigquery.NewClient(ctx, cfg.Project, option.WithQuotaProject(cfg.Project))
	if err != nil {
		return nil, errors.Wrap(err, "creating bigquery client")
	}
	defer client.Close()
	ds, err := workDataset(ctx, client)
	if err != nil {
		return nil, err
	}
	snap, err := latestSnapshot(ctx, client, ds)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(deps.IO.Err, "deps.dev snapshot %s\n", snap.Format(time.RFC3339))
	var recs []signals.PrevalenceRecord
	for _, name := range cfg.ecosystems() {
		pkgs, err := packagePrevalence(ctx, client, ds, name, snap, cfg.Top)
		if err != nil {
			return nil, errors.Wrapf(err, "querying %s package prevalence", name)
		}
		vers, err := versionPrevalence(ctx, client, ds, name, snap, cfg.TopVersions)
		if err != nil {
			return nil, errors.Wrapf(err, "querying %s version prevalence", name)
		}
		fmt.Fprintf(deps.IO.Err, "[%s] %d package(s), %d version(s)\n", name, len(pkgs), len(vers))
		recs = append(recs, pkgs...)
		recs = append(recs, vers...)
	}
	scored := scoreByEcosystem(recs)
	outFS, out, err := billyx.NewResolver().FS(ctx, cfg.Out)
	if err != nil {
		return nil, errors.Wrapf(err, "resolving %s", cfg.Out)
	}
	w, err := outFS.Create(out)
	if err != nil {
		return nil, errors.Wrapf(err, "opening %s", cfg.Out)
	}
	if err := jsonl.Encode(w, scored); err != nil {
		w.Close()
		return nil, errors.Wrap(err, "writing export")
	}
	if err := w.Close(); err != nil {
		return nil, errors.Wrapf(err, "finalizing %s", cfg.Out)
	}
	fmt.Fprintf(deps.IO.Out, "wrote %d prevalence record(s) to %s\n", len(scored), cfg.Out)
	return &act.NoOutput{}, nil
}

// scoreByEcosystem attaches per-ecosystem scores: each row's dependent count on
// a log scale relative to the ecosystem's most depended-on package. Scoring is
// per-ecosystem because raw counts are not comparable across registries:
// npm's graph is denser than RubyGems' by an order of magnitude, so a shared
// scale would bury every gem below the npm long tail. Package and version rows
// share the denominator, so a version never outscores its package and the gap
// between them is the share of the package's use that version carries.
// Package rows precede version rows, ecosystems are sorted, and ties break by
// name, so the export is stable to rerun.
func scoreByEcosystem(recs []signals.PrevalenceRecord) []signals.PrevalenceRecord {
	pkgsByEco := map[string][]signals.PrevalenceRecord{}
	versByEco := map[string][]signals.PrevalenceRecord{}
	for _, r := range recs {
		if r.Ecosystem == "" || r.Package == "" {
			continue
		}
		if r.Version == "" {
			pkgsByEco[r.Ecosystem] = append(pkgsByEco[r.Ecosystem], r)
		} else {
			versByEco[r.Ecosystem] = append(versByEco[r.Ecosystem], r)
		}
	}
	maxByEco := map[string]int64{}
	for _, byEco := range []map[string][]signals.PrevalenceRecord{pkgsByEco, versByEco} {
		for eco, rs := range byEco {
			for _, r := range rs {
				maxByEco[eco] = max(maxByEco[eco], r.Dependents)
			}
		}
	}
	var out []signals.PrevalenceRecord
	for _, byEco := range []map[string][]signals.PrevalenceRecord{pkgsByEco, versByEco} {
		for _, eco := range slices.Sorted(maps.Keys(byEco)) {
			scored := byDependents(byEco[eco])
			for i := range scored {
				scored[i].Prevalence = signals.LogScale(scored[i].Dependents, maxByEco[eco])
			}
			out = append(out, scored...)
		}
	}
	return out
}

// byDependents orders by descending dependent count with a name tiebreak. The
// queries apply the same total order to their LIMIT, so membership at the
// cutoff is deterministic and the export is byte-stable across reruns of the
// same snapshot.
func byDependents(recs []signals.PrevalenceRecord) []signals.PrevalenceRecord {
	slices.SortFunc(recs, func(a, b signals.PrevalenceRecord) int {
		return cmp.Or(
			cmp.Compare(b.Dependents, a.Dependents),
			cmp.Compare(a.Package, b.Package),
			cmp.Compare(a.Version, b.Version),
		)
	})
	return recs
}

// latestSnapshot reads the most recent deps.dev snapshot time. Pinning queries
// to it as a TIMESTAMP parameter lets BigQuery prune to a single partition.
func latestSnapshot(ctx context.Context, client *bigquery.Client, ds *bigquery.Dataset) (time.Time, error) {
	it, err := runQuery(ctx, client.Query("SELECT MAX(Time) AS Time FROM `bigquery-public-data.deps_dev_v1.Snapshots`"), ds.Table("latest_snapshot"))
	if err != nil {
		return time.Time{}, errors.Wrap(err, "reading deps.dev snapshots")
	}
	var row struct{ Time time.Time }
	if err := it.Next(&row); err != nil {
		return time.Time{}, errors.Wrap(err, "reading latest snapshot")
	}
	if row.Time.IsZero() {
		return time.Time{}, errors.New("no deps.dev snapshot found")
	}
	return row.Time, nil
}

// depsDevSystem resolves an ecosystem to the deps.dev `System` enum naming it.
func depsDevSystem(ecosystem string) (string, error) {
	system, ok := signals.EcosystemSystem[rebuild.Ecosystem(ecosystem)]
	if !ok {
		return "", errors.Errorf("ecosystem %q has no deps.dev dependency-graph coverage", ecosystem)
	}
	return system, nil
}

// packagePrevalence counts, for each package, the distinct packages whose
// resolved dependency graph contains it at the given snapshot: its transitive
// dependents. Every edge row names the root package version whose graph it
// belongs to, so this is a distinct count over that root, not a graph walk. A
// root's graph can contain another version of the root itself; those rows are
// excluded, as are bundled copies: deps.dev names a dependency vendored inside
// another package's tarball by its nesting path joined with ">", e.g.
// "react-scripts>0.5.1>babel-preset-react-app>regjsgen". Consumers receive
// those copies through the parent artifact, not the registry, so they are not
// rebuild targets and would otherwise inflate the population several-fold.
func packagePrevalence(ctx context.Context, client *bigquery.Client, ds *bigquery.Dataset, ecosystem string, snap time.Time, top int) ([]signals.PrevalenceRecord, error) {
	system, err := depsDevSystem(ecosystem)
	if err != nil {
		return nil, err
	}
	q := client.Query("SELECT T.`To`.Name AS Package, COUNT(DISTINCT T.Name) AS Dependents\n" +
		"FROM `bigquery-public-data.deps_dev_v1.DependencyGraphEdges` T\n" +
		"WHERE T.System = @system AND T.SnapshotAt = @snap\n" +
		"  AND T.Name != T.`To`.Name\n" +
		"  AND T.`To`.Name NOT LIKE '%>%'\n" +
		"GROUP BY Package\n" +
		"ORDER BY Dependents DESC, Package\n" +
		"LIMIT @top")
	q.Parameters = []bigquery.QueryParameter{
		{Name: "system", Value: system},
		{Name: "snap", Value: snap},
		{Name: "top", Value: top},
	}
	it, err := runQuery(ctx, q, ds.Table("pkg_prevalence_"+ecosystem))
	if err != nil {
		return nil, err
	}
	var out []signals.PrevalenceRecord
	for {
		var row struct {
			Package    string
			Dependents int64
		}
		switch err := it.Next(&row); err {
		case nil:
			out = append(out, signals.PrevalenceRecord{Ecosystem: ecosystem, Package: row.Package, Dependents: row.Dependents})
		case iterator.Done:
			return out, nil
		default:
			return nil, errors.Wrap(err, "iterating results")
		}
	}
}

// versionPrevalence counts, for each exact version, the distinct packages
// whose resolved dependency graph contains that version. Same table, snapshot
// and exclusions as packagePrevalence, grouped one column finer.
func versionPrevalence(ctx context.Context, client *bigquery.Client, ds *bigquery.Dataset, ecosystem string, snap time.Time, top int) ([]signals.PrevalenceRecord, error) {
	system, err := depsDevSystem(ecosystem)
	if err != nil {
		return nil, err
	}
	q := client.Query("SELECT T.`To`.Name AS Package, T.`To`.Version AS Version, COUNT(DISTINCT T.Name) AS Dependents\n" +
		"FROM `bigquery-public-data.deps_dev_v1.DependencyGraphEdges` T\n" +
		"WHERE T.System = @system AND T.SnapshotAt = @snap\n" +
		"  AND T.Name != T.`To`.Name\n" +
		"  AND T.`To`.Name NOT LIKE '%>%'\n" +
		"  AND T.`To`.Version != ''\n" +
		"GROUP BY Package, Version\n" +
		"ORDER BY Dependents DESC, Package, Version\n" +
		"LIMIT @top")
	q.Parameters = []bigquery.QueryParameter{
		{Name: "system", Value: system},
		{Name: "snap", Value: snap},
		{Name: "top", Value: top},
	}
	it, err := runQuery(ctx, q, ds.Table("ver_prevalence_"+ecosystem))
	if err != nil {
		return nil, err
	}
	var out []signals.PrevalenceRecord
	for {
		var row struct {
			Package    string
			Version    string
			Dependents int64
		}
		switch err := it.Next(&row); err {
		case nil:
			out = append(out, signals.PrevalenceRecord{Ecosystem: ecosystem, Package: row.Package, Version: row.Version, Dependents: row.Dependents})
		case iterator.Done:
			return out, nil
		default:
			return nil, errors.Wrap(err, "iterating results")
		}
	}
}

// workDataset returns the dataset that holds query results, creating it if
// missing. Without an explicit destination BigQuery materializes results
// into a hidden per-user dataset whose ACL names the querying user, which
// domain-restricted organizations reject at creation; a dataset granted
// only the project's special groups never names a user, so it is allowed.
func workDataset(ctx context.Context, client *bigquery.Client) (*bigquery.Dataset, error) {
	ds := client.Dataset("onboard_prevalence")
	if _, err := ds.Metadata(ctx); err == nil {
		return ds, nil
	}
	err := ds.Create(ctx, &bigquery.DatasetMetadata{
		Description:            "Query results for ctl onboard priority prevalence.",
		DefaultTableExpiration: 24 * time.Hour,
		Access: []*bigquery.AccessEntry{
			{Role: bigquery.OwnerRole, EntityType: bigquery.SpecialGroupEntity, Entity: "projectOwners"},
			{Role: bigquery.WriterRole, EntityType: bigquery.SpecialGroupEntity, Entity: "projectWriters"},
			{Role: bigquery.ReaderRole, EntityType: bigquery.SpecialGroupEntity, Entity: "projectReaders"},
		},
	})
	return ds, errors.Wrap(err, "creating work dataset")
}

func runQuery(ctx context.Context, q *bigquery.Query, dst *bigquery.Table) (*bigquery.RowIterator, error) {
	q.Dst = dst
	q.WriteDisposition = bigquery.WriteTruncate
	job, err := q.Run(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "running query")
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "waiting for query")
	}
	if err := status.Err(); err != nil {
		return nil, errors.Wrap(err, "query failed")
	}
	it, err := job.Read(ctx)
	return it, errors.Wrap(err, "reading results")
}

func prevalenceCommand() *cobra.Command {
	cfg := prevalenceConfig{}
	cmd := &cobra.Command{
		Use:   "prevalence --project <project> --out <uri> [--ecosystems <eco,...>] [--top <n>] [--top-versions <n>]",
		Short: "Rank packages by distinct transitive dependents (deps.dev graph)",
		Long: `Rank packages by distinct transitive dependents (deps.dev graph).

Counts how many distinct packages depend, directly or transitively, on each
package, and separately on each exact version, at the latest deps.dev
snapshot, then scores both on a log scale relative to the ecosystem's most
depended-on package and writes the result as a JSONL export.

Reads public data and writes externally so no BigQuery write access required.`,
		Args: cobra.NoArgs,
		RunE: cli.RunE(&cfg, cli.SkipArgs[prevalenceConfig], InitDeps, prevalenceHandler),
	}
	set := flag.NewFlagSet(cmd.Name(), flag.ContinueOnError)
	set.StringVar(&cfg.Project, "project", "", "GCP project that runs and is billed for the BigQuery jobs")
	set.StringVar(&cfg.Ecosystems, "ecosystems", "npm,pypi,cratesio,rubygems,maven", "comma-separated ecosystems to rank")
	set.IntVar(&cfg.Top, "top", 5000, "max packages to keep per ecosystem, by dependent count")
	set.IntVar(&cfg.TopVersions, "top-versions", 20000, "max versions to keep per ecosystem, by dependent count")
	set.StringVar(&cfg.Out, "out", "", "write the ranked JSONL export to this path or gs:// URI")
	cmd.Flags().AddGoFlagSet(set)
	return cmd
}
