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
	// Only populate the status grid at the top if deps.Tracked is provided.
	if deps.Tracked != nil {
		benchRebuilds, err := deps.Rundex.LatestTrackedPackages(ctx, deps.Tracked)
		if err != nil {
			return nil, errors.Wrap(err, "fetching benchmark rebuilds")
		}
		applySuccessRegex(deps.SuccessRegex, benchRebuilds)
		ran := feed.TrackedPackageIndex{}
		success := feed.TrackedPackageIndex{}
		for _, rb := range benchRebuilds {
			eco := rebuild.Ecosystem(rb.Ecosystem)
			if _, ok := ran[eco]; !ok {
				ran[eco] = make(map[string]bool)
			}
			ran[eco][rb.Package] = true
			if !rb.Success {
				continue
			}
			if _, ok := success[eco]; !ok {
				success[eco] = make(map[string]bool)
			}
			success[eco][rb.Package] = true
		}
		var targets []BenchmarkTarget
		var stats BenchmarkStats
		for eco, pkgs := range deps.Tracked {
			for pkg := range pkgs {
				stats.Total++
				hasRun := ran[eco][pkg]
				success := success[eco][pkg]
				if hasRun && success {
					stats.Success++
				} else if hasRun && !success {
					stats.Failed++
				}
				et := packagePathEncoding.Encode(rebuild.Target{
					Ecosystem: eco,
					Package:   pkg,
				})
				targets = append(targets, BenchmarkTarget{
					Ecosystem:        string(eco),
					Package:          pkg,
					EncodedEcosystem: string(et.Ecosystem),
					EncodedPackage:   et.Package,
					Success:          success,
					HasRun:           hasRun,
				})
			}
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
	if deps.Tracked != nil {
		// Fetch recent rebuilds filtered by Tracked
		rebuilds, err = deps.Rundex.FetchRebuilds(ctx, &rundex.FetchRebuildRequest{
			Tracked: deps.Tracked,
			Limit:   100,
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
