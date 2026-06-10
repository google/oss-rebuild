// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratchworkerservice

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
)

// StatRequest is the empty input for the worker's /stat endpoint.
type StatRequest struct{}

// Validate implements act.Input.
func (StatRequest) Validate() error { return nil }

// DiskUsage reports total/free byte counts for a mounted filesystem.
type DiskUsage struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// StatResult is the worker self-report returned by /stat.
type StatResult struct {
	DockerSocketPresent bool        `json:"docker_socket_present"`
	DockerSocketPath    string      `json:"docker_socket_path,omitempty"`
	Disks               []DiskUsage `json:"disks,omitempty"`
}

// StatDeps are the inputs the Stat handler needs.
// Configured at worker startup, the same values are used on every request.
type StatDeps struct {
	// DockerSocketPath is the path probed for daemon presence.
	// Default at construction time, typically "/var/run/docker.sock".
	DockerSocketPath string
	// DiskPaths are the filesystem mounts to report usage for.
	// Typically Workdir + the Docker root.
	DiskPaths []string
}

// Stat returns a snapshot of the worker's relevant local state.
func Stat(_ context.Context, _ StatRequest, deps *StatDeps) (*StatResult, error) {
	out := &StatResult{}
	if deps.DockerSocketPath != "" {
		out.DockerSocketPath = deps.DockerSocketPath
		if _, err := os.Stat(deps.DockerSocketPath); err == nil {
			out.DockerSocketPresent = true
		}
	}
	for _, p := range deps.DiskPaths {
		if du, err := diskUsage(p); err == nil {
			out.Disks = append(out.Disks, du)
		}
	}
	return out, nil
}

func diskUsage(path string) (DiskUsage, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return DiskUsage{}, err
	}
	bsize := uint64(s.Bsize)
	return DiskUsage{
		Path:  path,
		Total: bsize * uint64(s.Blocks),
		Free:  bsize * uint64(s.Bavail),
	}, nil
}
