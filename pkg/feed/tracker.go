// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/rebuild/schema"
)

type Tracker interface {
	IsTracked(schema.TargetEvent) (bool, error)
}

type funcTracker struct {
	isTracked func(schema.TargetEvent) (bool, error)
}

func (f *funcTracker) IsTracked(e schema.TargetEvent) (bool, error) {
	return f.isTracked(e)
}

var _ Tracker = &funcTracker{}

func TrackerFromFunc(isTracked func(schema.TargetEvent) (bool, error)) Tracker {
	return &funcTracker{isTracked: isTracked}
}

// TrackedPackageSet is a map from ecosystem to list of packages. Optimized for storage, not access.
type TrackedPackageSet map[rebuild.Ecosystem][]string

// TrackedPackageIndex is a map from ecosystem to a set of tracked package names. Optimized for access, not storage.
type TrackedPackageIndex map[rebuild.Ecosystem]map[string]bool
