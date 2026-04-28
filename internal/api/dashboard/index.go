// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/feed"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

var _ api.HandlerT[IndexRequest, IndexData, *Deps] = Index

type BenchmarkStats struct {
	Total   int
	Success int
	Failed  int
}

// BenchmarkTarget is a tracked target (version and artifact agnostic)
type BenchmarkTarget struct {
	Ecosystem        string
	Package          string
	EncodedEcosystem string
	EncodedPackage   string
	Success          bool
	HasRun           bool
}

type IndexRequest struct{}

func (IndexRequest) Validate() error { return nil }

type IndexData struct {
	BenchmarkTitle   string
	BenchmarkStats   BenchmarkStats
	BenchmarkTargets []BenchmarkTarget
	RecentRebuilds   []RebuildView
}

func Index(ctx context.Context, _ IndexRequest, deps *Deps) (*IndexData, error) {
	data := IndexData{}
	if deps.Benchmark != nil {
		// Compile benchmark stats
		tracked := make(feed.TrackedPackageIndex)
		for _, p := range deps.Benchmark.Packages {
			eco := rebuild.Ecosystem(p.Ecosystem)
			if _, ok := tracked[eco]; !ok {
				tracked[eco] = make(map[string]bool)
			}
			tracked[eco][p.Name] = true
		}

		benchRebuilds, err := deps.Rundex.LatestTrackedPackages(ctx, tracked)
		if err != nil {
			return nil, errors.Wrap(err, "fetching benchmark rebuilds")
		}

		applySuccessRegex(deps.SuccessRegex, benchRebuilds)

		successMap := make(map[string]bool)
		for _, rb := range benchRebuilds {
			key := rb.Ecosystem + ":" + rb.Package
			successMap[key] = rb.Success
		}

		var targets []BenchmarkTarget
		stats := BenchmarkStats{Total: deps.Benchmark.Count}

		for _, p := range deps.Benchmark.Packages {
			key := p.Ecosystem + ":" + p.Name
			success, hasRun := successMap[key]
			if hasRun && success {
				stats.Success++
			} else if hasRun && !success {
				stats.Failed++
			}

			et := packagePathEncoding.Encode(rebuild.Target{
				Ecosystem: rebuild.Ecosystem(p.Ecosystem),
				Package:   p.Name,
			})

			targets = append(targets, BenchmarkTarget{
				Ecosystem:        p.Ecosystem,
				Package:          p.Name,
				EncodedEcosystem: string(et.Ecosystem),
				EncodedPackage:   et.Package,
				Success:          success,
				HasRun:           hasRun,
			})
		}

		slices.SortFunc(targets, func(a, b BenchmarkTarget) int {
			if a.Ecosystem != b.Ecosystem {
				return strings.Compare(a.Ecosystem, b.Ecosystem)
			}
			return strings.Compare(a.Package, b.Package)
		})

		data.BenchmarkTitle = deps.BenchmarkName
		data.BenchmarkStats = stats
		data.BenchmarkTargets = targets
	}

	var rebuilds []rundex.Rebuild
	var err error
	if deps.Benchmark != nil {
		// Fetch recent rebuilds filtered by benchmark
		rebuilds, err = deps.Rundex.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{
			Bench: deps.Benchmark,
			Limit: 100,
		})
		if err != nil {
			return nil, errors.Wrap(err, "fetching benchmark filtered rebuilds")
		}
	} else {
		// Fetch recent global rebuild attempts.
		rebuilds, err = deps.Rundex.RecentRebuilds(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "fetching rebuilds")
		}
	}
	applySuccessRegex(deps.SuccessRegex, rebuilds)

	data.RecentRebuilds = make([]RebuildView, len(rebuilds))
	for i, rb := range rebuilds {
		data.RecentRebuilds[i] = NewRebuildView(rb)
	}
	return &data, nil
}
