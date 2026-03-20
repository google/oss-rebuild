// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

// DockerBuildPlan represents a Docker build execution plan where we build an image and run it
type DockerBuildPlan struct {
	// Dockerfile contains the generated Dockerfile content
	Dockerfile string
	// ContextDir specifies the local directory to use as the build context.
	// If empty, no context is passed and stdin '-' is used as the context.
	ContextDir string
	// OutputPath specifies where artifacts should be copied from the container
	OutputPath string
	// Indicates whether to run the container in privileged mode
	Privileged bool
}
