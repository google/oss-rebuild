// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"time"

	"github.com/google/oss-rebuild/internal/semver"
	"github.com/pkg/errors"
)

//go:generate go run gen_node_releases.go

type NodeRelease struct {
	Version semver.Semver
	NPM     semver.Semver // bundled npm version
	Date    time.Time
}

func newNodeRelease(version, npm, dateStr string) NodeRelease {
	sv, err := semver.New(version)
	if err != nil {
		panic(errors.Wrapf(err, "parsing version '%s'", version))
	}
	nv, err := semver.New(npm)
	if err != nil {
		panic(errors.Wrapf(err, "parsing npm version '%s'", npm))
	}
	date, err := time.Parse(time.DateOnly, dateStr)
	if err != nil {
		panic(errors.Wrapf(err, "parsing date '%s'", dateStr))
	}
	return NodeRelease{Version: sv, NPM: nv, Date: date}
}
