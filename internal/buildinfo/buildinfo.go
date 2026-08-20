// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package buildinfo holds build identity embedded at link time.
//
// Release builds set these via -ldflags, e.g.:
//
//	$ go build -ldflags "\
//	    -X github.com/google/oss-rebuild/internal/buildinfo.Repo=https://github.com/google/oss-rebuild \
//	    -X github.com/google/oss-rebuild/internal/buildinfo.Version=v0.0.0-20240101120000-abcdef123456"
//
// Both are empty in unstamped builds (local `go build`, tests, tooling).
package buildinfo

var (
	// Repo is the canonical source repository URI of this build.
	Repo string
	// Version is the Go module pseudo-version of this build.
	Version string
)
