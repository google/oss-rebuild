package feed

import (
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

func GroupForSmoketest(targets []rebuild.Target, runID string) []schema.SmoketestRequest {
	var res []schema.SmoketestRequest
	grouped := map[rebuild.Ecosystem]map[string][]rebuild.Target{}
	for _, t := range targets {
		if _, ok := grouped[t.Ecosystem]; !ok {
			grouped[t.Ecosystem] = map[string][]rebuild.Target{}
		}
		if _, ok := grouped[t.Ecosystem][t.Package]; !ok {
			grouped[t.Ecosystem][t.Package] = []rebuild.Target{}
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
		}
	}
	return res
}

func GroupForAttest(targets []rebuild.Target, runID string) []schema.RebuildPackageRequest {
	var res []schema.RebuildPackageRequest
	for _, t := range targets {
		res = append(res,
			schema.RebuildPackageRequest{
				Ecosystem: t.Ecosystem,
				Package:   t.Package,
				Version:   t.Version,
				Artifact:  t.Artifact,
				ID:        runID,
			},
		)
	}
	return res
}
