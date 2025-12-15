// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package benchmark

import (
	"path"
	"sort"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func packageSetToTargetMap(ps PackageSet) (map[string]rebuild.Target, error) {
	targetMap := make(map[string]rebuild.Target)
	for _, p := range ps.Packages {
		for i, v := range p.Versions {
			t := rebuild.Target{
				Ecosystem: rebuild.Ecosystem(p.Ecosystem),
				Package:   p.Name,
				Version:   v,
			}
			if len(p.Artifacts) > i {
				t.Artifact = p.Artifacts[i]
			}
			et := rebuild.FilesystemTargetEncoding.Encode(t)
			key := path.Join(string(et.Ecosystem), et.Package, et.Version, et.Artifact)
			targetMap[key] = t
		}
	}
	return targetMap, nil
}

func targetMapToPackageSet(targetMap map[string]rebuild.Target) PackageSet {
	packages := make(map[string]map[string][]rebuild.Target) // ecosystem -> package -> targets
	for _, t := range targetMap {
		if _, ok := packages[string(t.Ecosystem)]; !ok {
			packages[string(t.Ecosystem)] = make(map[string][]rebuild.Target)
		}
		packages[string(t.Ecosystem)][t.Package] = append(packages[string(t.Ecosystem)][t.Package], t)
	}
	var ps PackageSet
	for eco, pkgs := range packages {
		for name, targets := range pkgs {
			p := Package{
				Ecosystem: eco,
				Name:      name,
			}
			sort.Slice(targets, func(i, j int) bool {
				if targets[i].Version != targets[j].Version {
					return targets[i].Version < targets[j].Version
				}
				return targets[i].Artifact < targets[j].Artifact
			})
			for _, t := range targets {
				p.Versions = append(p.Versions, t.Version)
				if t.Artifact != "" {
					p.Artifacts = append(p.Artifacts, t.Artifact)
				}
			}
			if len(p.Versions) > 0 {
				ps.Packages = append(ps.Packages, p)
			}
		}
	}
	sort.Slice(ps.Packages, func(i, j int) bool {
		if ps.Packages[i].Ecosystem != ps.Packages[j].Ecosystem {
			return ps.Packages[i].Ecosystem < ps.Packages[j].Ecosystem
		}
		return ps.Packages[i].Name < ps.Packages[j].Name
	})
	ps.Metadata.Count = len(targetMap)
	ps.Metadata.Updated = time.Now().UTC()
	return ps
}

func Add(left, right PackageSet) (PackageSet, error) {
	leftMap, err := packageSetToTargetMap(left)
	if err != nil {
		return PackageSet{}, err
	}
	rightMap, err := packageSetToTargetMap(right)
	if err != nil {
		return PackageSet{}, err
	}
	for k, v := range rightMap {
		leftMap[k] = v
	}
	return targetMapToPackageSet(leftMap), nil
}

func Subtract(left, right PackageSet) (PackageSet, error) {
	baseMap, err := packageSetToTargetMap(left)
	if err != nil {
		return PackageSet{}, err
	}
	subtractMap, err := packageSetToTargetMap(right)
	if err != nil {
		return PackageSet{}, err
	}
	for k := range subtractMap {
		delete(baseMap, k)
	}
	return targetMapToPackageSet(baseMap), nil
}
