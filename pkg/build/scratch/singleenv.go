// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package scratch

import (
	"path"

	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/build/local"
)

// Single-environment build directories, mounted read-only in the retained
// build container so the agent needs no host shell to reach them.
const (
	ContainerToolsDir    = "/tools"    // diagnosis tools, e.g. diffr
	ContainerUpstreamDir = "/upstream" // upstream artifact
	ContainerBuildsDir   = "/builds"   // every attempt's staging tree
)

// Host directories under WorkDir behind ContainerToolsDir and
// ContainerUpstreamDir. ScratchRunner.diffOnVM fetches diffr and the upstream
// artifact into them.
const (
	toolsSubdir    = "tools"
	upstreamSubdir = "upstream"
)

// ToolsDir is the host directory holding the diagnosis tools on the VM.
func ToolsDir(workDir string) string { return path.Join(workDir, toolsSubdir) }

// UpstreamDir is the host directory holding the upstream artifact on the VM.
func UpstreamDir(workDir string) string { return path.Join(workDir, upstreamSubdir) }

// DiffrPath is where diffr is fetched on the VM host.
func DiffrPath(workDir string) string { return path.Join(ToolsDir(workDir), string(build.DiffrTool)) }

// ContainerDiffrPath is diffr's path inside the container.
func ContainerDiffrPath() string { return path.Join(ContainerToolsDir, string(build.DiffrTool)) }

// ContainerToolPath is a prebuilt tool's path inside the container.
func ContainerToolPath(tool build.ToolType) string { return path.Join(ContainerToolsDir, string(tool)) }

// ContainerUpstreamPath is the upstream artifact's container path.
func ContainerUpstreamPath(artifactBase string) string {
	return path.Join(ContainerUpstreamDir, artifactBase)
}

// ContainerArtifactPath is the rebuilt artifact's path inside the container.
func ContainerArtifactPath() string { return local.DockerRunOutputPath }

// singleEnvMounts are read-only, which still lets diffr run from the tools
// mount since the host /home is mounted exec.
func singleEnvMounts(workDir string) []local.Mount {
	return []local.Mount{
		{HostPath: ToolsDir(workDir), ContainerPath: ContainerToolsDir},
		{HostPath: UpstreamDir(workDir), ContainerPath: ContainerUpstreamDir},
		{HostPath: path.Join(workDir, buildsSubdir), ContainerPath: ContainerBuildsDir},
	}
}
