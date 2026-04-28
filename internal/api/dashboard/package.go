// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"context"
	"slices"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/internal/rundex"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

var _ api.HandlerT[PackageRequest, PackageData, *Deps] = Package

type PackageRequest struct {
	Ecosystem string
	Package   string
}

func (PackageRequest) Validate() error { return nil }

type PackageData struct {
	Ecosystem       string
	PackageName     string
	EncodedPackage  string
	PackageRebuilds []RebuildView
}

func Package(ctx context.Context, req PackageRequest, deps *Deps) (*PackageData, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Fetch rebuild attempts for this specific package.
	rebuilds, err := deps.Rundex.RecentPackageRebuilds(ctx, rebuild.Ecosystem(req.Ecosystem), req.Package)
	if err != nil {
		return nil, errors.Wrap(err, "fetching rebuilds")
	}

	applySuccessRegex(deps.SuccessRegex, rebuilds)

	// Sort rebuilds by creation time descending (most recent first)
	slices.SortFunc(rebuilds, func(a, b rundex.Rebuild) int {
		return b.Created.Compare(a.Created)
	})

	views := make([]RebuildView, len(rebuilds))
	for i, rb := range rebuilds {
		views[i] = NewRebuildView(rb)
	}

	et := packagePathEncoding.Encode(rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
	})

	return &PackageData{
		Ecosystem:       req.Ecosystem,
		PackageName:     req.Package,
		EncodedPackage:  et.Package,
		PackageRebuilds: views,
	}, nil
}
