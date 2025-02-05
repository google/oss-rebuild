// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
