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

// Package docker contains container execution APIs.
package docker

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"syscall"
)

// RunOptions defines optional arguments for RunServer.
type RunOptions struct {
	ID         chan<- string
	Output     io.Writer
	DockerArgs []string
	Args       []string
}

// RunServer runs a docker container hosting a simple server.
func RunServer(ctx context.Context, img string, port int, opts *RunOptions) error {
	args := append([]string{"run", "--detach", "-p", fmt.Sprintf("%d:%d", port, port), "--rm"}, opts.DockerArgs...)
	args = append(args, []string{img, "--user-agent=OSSRebuildLocal/0.0.0"}...)
	args = append(args, opts.Args...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	log.Print(cmd.String())
	out, err := cmd.Output()
	if err != nil {
		if opts.ID != nil {
			close(opts.ID)
		}
		return err
	}
	containerID := strings.TrimSpace(string(out))
	if opts.ID != nil {
		opts.ID <- containerID
		close(opts.ID)
	}

	// Attach to the container to receive logs.
	cmd = exec.CommandContext(ctx, "docker", "attach", containerID)
	cmd.Stdout = opts.Output
	cmd.Stderr = opts.Output
	// NOTE: The default sends SIGKILL which is not proxied to the container.
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGINT)
	}
	return cmd.Run()
}
