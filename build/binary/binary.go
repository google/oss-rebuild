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

// Package binary provides routines to programmatically build binary components of the project.
package binary

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Build constructs a binary for one of the project's microservices.
func Build(ctx context.Context, name string) (path string, err error) {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", strings.Join([]string{".", "bin", name}, string(os.PathSeparator)), strings.Join([]string{".", "cmd", name}, string(os.PathSeparator)))
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	cmd.Stdout = log.Writer()
	cmd.Stderr = log.Writer()
	log.Print(cmd.String())
	err = cmd.Run()
	if err != nil {
		return
	}
	path, err = filepath.Abs(filepath.Join(".", "bin", name))
	return
}
