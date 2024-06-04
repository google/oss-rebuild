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
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// Build constructs a container for one of the project's microservices.
func Build(ctx context.Context, name, binary string) error {
	tempDir, err := os.MkdirTemp("", "oss-rebuild")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	err = copyFile(filepath.Join(tempDir, name), filepath.Join("./bin/", name))
	if err != nil {
		return err
	}

	// Build the Docker image.
	relpath := "build/package/Dockerfile." + name
	dockerfile, _ := filepath.Abs(relpath)
	cmd := exec.CommandContext(ctx, "docker", "build", "--build-arg", "BINARY="+name, "--tag", name, "--file", dockerfile, tempDir)
	cmd.Stdout = log.Writer()
	cmd.Stderr = log.Writer()
	log.Print(cmd.String())
	return cmd.Run()
}

func copyFile(dst, src string) error {
	orig, err := os.Open(src)
	if err != nil {
		return err
	}
	defer orig.Close()
	dest, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err != nil {
		return err
	}
	defer dest.Close()
	_, err = io.Copy(dest, orig)
	return err
}
