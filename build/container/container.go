// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package container provides routines to programmatically build container components of the project.
package container

import (
	"context"
	"log"
	"os/exec"
	"path/filepath"
)

// Build constructs a container for one of the project's microservices.
func Build(ctx context.Context, name string) error {
	// Build the Docker image.
	relpath := "build/package/Dockerfile." + name
	dockerfile, _ := filepath.Abs(relpath)
	cmd := exec.CommandContext(ctx, "docker", "build", "--tag", name, "--file", dockerfile, ".")
	cmd.Stdout = log.Writer()
	cmd.Stderr = log.Writer()
	log.Print(cmd.String())
	return cmd.Run()
}
