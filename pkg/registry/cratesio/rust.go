// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"fmt"
	"time"

	"github.com/pkg/errors"
)

//go:generate go run gen_rust_releases.go

type release struct {
	Version   string
	Date      time.Time
	MUSLBuild bool
}

func newRelease(version, dateStr string, muslBuild bool) release {
	date, err := time.Parse(time.DateOnly, dateStr)
	if err != nil {
		panic(fmt.Sprintf("Error parsing date '%s': %v", dateStr, err))
	}
	return release{Version: version, Date: date, MUSLBuild: muslBuild}
}

// RustVersionAt returns the rust version as of the provided date.
func RustVersionAt(t time.Time) (string, error) {
	for _, r := range releases {
		if r.Date.After(t) {
			continue
		}
		return r.Version, nil
	}
	return "", errors.New("no Rust release found")
}

func HasMUSLBuild(version string) (bool, error) {
	for _, r := range releases {
		if r.Version == version {
			return r.MUSLBuild, nil
		}
	}
	return false, errors.New("no Rust release found")
}

// EarliestMUSLBuild returns the oldest release that ships a MUSL toolchain.
func EarliestMUSLBuild() string {
	earliest := ""
	for _, r := range releases {
		if r.MUSLBuild {
			earliest = r.Version
		}
	}
	return earliest
}
