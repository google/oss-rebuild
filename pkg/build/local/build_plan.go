// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

// DockerBuildPlan represents a Docker build execution plan where we build an image and run it
type DockerBuildPlan struct {
	// Dockerfile contains the generated Dockerfile content
	Dockerfile string
	// OutputPath specifies where artifacts should be copied from the container
	OutputPath string
}
