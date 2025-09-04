// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

// DockerRunPlan represents a Docker run execution plan where we run an existing image
type DockerRunPlan struct {
	// Image is the Docker image to run
	Image string
	// Script is the bash script to execute inside the container
	Script string
	// WorkingDir sets the working directory in the container
	WorkingDir string
	// OutputPath specifies where artifacts should be copied from the container
	OutputPath string
}
