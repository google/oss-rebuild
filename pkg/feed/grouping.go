// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"cmp"
	"slices"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func GroupForSmoketest(targets []rebuild.Target, runID string) []schema.SmoketestRequest {
	if len(targets) == 0 {
		return nil
	}
	var res []schema.SmoketestRequest
	grouped := make(map[rebuild.Ecosystem]map[string][]rebuild.Target)
	for _, t := range targets {
		if _, ok := grouped[t.Ecosystem]; !ok {
			grouped[t.Ecosystem] = make(map[string][]rebuild.Target)
		}
		grouped[t.Ecosystem][t.Package] = append(grouped[t.Ecosystem][t.Package], t)
	}
	for e, pkgs := range grouped {
		for p, targets := range pkgs {
			req := schema.SmoketestRequest{
				Ecosystem: e,
				Package:   p,
				Versions:  []string{},
				ID:        runID,
			}
			for _, t := range targets {
				req.Versions = append(req.Versions, t.Version)
			}
			slices.Sort(req.Versions)
			res = append(res, req)
		}
	}
	// Sort the final result for stability
	slices.SortFunc(res, func(a, b schema.SmoketestRequest) int {
		return cmp.Or(
			strings.Compare(string(a.Ecosystem), string(b.Ecosystem)),
			strings.Compare(a.Package, b.Package),
		)
	})
	return res
}

func GroupForAttest(targets []rebuild.Target, runID string) []schema.RebuildPackageRequest {
	if len(targets) == 0 {
		return nil
	}
	res := make([]schema.RebuildPackageRequest, len(targets))
	for i, t := range targets {
		res[i] = schema.RebuildPackageRequest{
			Ecosystem: t.Ecosystem,
			Package:   t.Package,
			Version:   t.Version,
			Artifact:  t.Artifact,
			ID:        runID,
		}
	}
	return res
}
