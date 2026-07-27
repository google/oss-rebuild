// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func runArgsTestPlan() *DockerRunPlan {
	return &DockerRunPlan{
		Image:      "alpine:3.19",
		Setup:      "echo setup",
		Source:     "echo source",
		Deps:       "echo deps",
		Build:      "echo build",
		WorkingDir: "/workspace",
		OutputPath: "/out/rebuild",
	}
}

func TestComposeDockerStartArgs(t *testing.T) {
	for _, tt := range []struct {
		name string
		plan *DockerRunPlan
		opts RunArgsOpts
		want []string
	}{
		{
			name: "local defaults",
			plan: runArgsTestPlan(),
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/tmp/oss-rebuild-b1",
			},
			want: []string{"run", "--detach", "--name", "b1", "-v", "/tmp/oss-rebuild-b1:/out", "-w", "/workspace", "--ulimit", "core=0", "alpine:3.19", "sleep", "infinity"},
		},
		{
			name: "inline auth",
			plan: runArgsTestPlan(),
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/tmp/b1",
				AuthMode:       AuthInline,
				AuthValue:      "Authorization: Bearer tok",
			},
			want: []string{"run", "--detach", "--name", "b1", "-v", "/tmp/b1:/out", "-w", "/workspace", "-e", "AUTH_HEADER=Authorization: Bearer tok", "--ulimit", "core=0", "alpine:3.19", "sleep", "infinity"},
		},
		{
			name: "removed on exit",
			plan: runArgsTestPlan(),
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/tmp/b1",
				Remove:         true,
			},
			want: []string{"run", "--detach", "--rm", "--name", "b1", "-v", "/tmp/b1:/out", "-w", "/workspace", "--ulimit", "core=0", "alpine:3.19", "sleep", "infinity"},
		},
		{
			name: "privileged allowed",
			plan: &DockerRunPlan{Image: "alpine:3.19", Build: "true", OutputPath: "/out/rebuild", Privileged: true},
			opts: RunArgsOpts{
				ContainerName:   "b1",
				OutputMountSrc:  "/tmp/b1",
				AllowPrivileged: true,
			},
			want: []string{"run", "--detach", "--name", "b1", "-v", "/tmp/b1:/out", "--privileged", "-v", "/var/run/docker.sock:/var/run/docker.sock", "--ulimit", "core=0", "alpine:3.19", "sleep", "infinity"},
		},
		{
			name: "privileged denied",
			plan: &DockerRunPlan{Image: "alpine:3.19", Build: "true", OutputPath: "/out/rebuild", Privileged: true},
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/tmp/b1",
			},
			want: []string{"run", "--detach", "--name", "b1", "-v", "/tmp/b1:/out", "--ulimit", "core=0", "alpine:3.19", "sleep", "infinity"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, ComposeDockerStartArgs(tt.plan, tt.opts)); diff != "" {
				t.Errorf("args mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestComposeDockerExecArgs(t *testing.T) {
	want := []string{"exec", "b1", "/bin/sh", "-c", "set -eux\necho setup"}
	if diff := cmp.Diff(want, ComposeDockerExecArgs("b1", Phase{"setup", "echo setup"})); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
}
