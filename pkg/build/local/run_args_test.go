// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestComposeDockerRunArgs(t *testing.T) {
	plan := &DockerRunPlan{
		Image:      "alpine:3.19",
		Script:     "echo hello",
		WorkingDir: "/workspace",
		OutputPath: "/out/rebuild",
	}
	for _, tt := range []struct {
		name    string
		plan    *DockerRunPlan
		opts    RunArgsOpts
		want    []string
		wantErr string
	}{
		{
			name: "local defaults",
			plan: plan,
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/tmp/oss-rebuild-b1",
				Remove:         true,
			},
			want: []string{"run", "--rm", "--name", "b1", "-v", "/tmp/oss-rebuild-b1:/out", "-w", "/workspace", "--ulimit", "core=0", "alpine:3.19", "/bin/sh", "-c", "echo hello"},
		},
		{
			name: "inline auth",
			plan: plan,
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/tmp/b1",
				Remove:         true,
				AuthMode:       AuthInline,
				AuthValue:      "Authorization: Bearer tok",
			},
			want: []string{"run", "--rm", "--name", "b1", "-v", "/tmp/b1:/out", "-w", "/workspace", "-e", "AUTH_HEADER=Authorization: Bearer tok", "--ulimit", "core=0", "alpine:3.19", "/bin/sh", "-c", "echo hello"},
		},
		{
			name: "env passthrough auth",
			plan: plan,
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/home/builder/builds/b1/out",
				Remove:         true,
				AuthMode:       AuthEnvPassthrough,
			},
			want: []string{"run", "--rm", "--name", "b1", "-v", "/home/builder/builds/b1/out:/out", "-w", "/workspace", "-e", "AUTH_HEADER", "--ulimit", "core=0", "alpine:3.19", "/bin/sh", "-c", "echo hello"},
		},
		{
			name: "privileged allowed",
			plan: &DockerRunPlan{Image: "alpine:3.19", Script: "true", OutputPath: "/out/rebuild", Privileged: true},
			opts: RunArgsOpts{
				ContainerName:   "b1",
				OutputMountSrc:  "/tmp/b1",
				AllowPrivileged: true,
			},
			want: []string{"run", "--name", "b1", "-v", "/tmp/b1:/out", "--privileged", "-v", "/var/run/docker.sock:/var/run/docker.sock", "--ulimit", "core=0", "alpine:3.19", "/bin/sh", "-c", "true"},
		},
		{
			name: "privileged denied",
			plan: &DockerRunPlan{Image: "alpine:3.19", Script: "true", OutputPath: "/out/rebuild", Privileged: true},
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/tmp/b1",
			},
			want: []string{"run", "--name", "b1", "-v", "/tmp/b1:/out", "--ulimit", "core=0", "alpine:3.19", "/bin/sh", "-c", "true"},
		},
		{
			name: "keepalive wraps script",
			plan: plan,
			opts: RunArgsOpts{
				ContainerName:  "b1",
				OutputMountSrc: "/tmp/b1",
				KeepAlive:      true,
			},
			want: []string{"run", "--name", "b1", "-v", "/tmp/b1:/out", "-w", "/workspace", "--ulimit", "core=0", "alpine:3.19", "/bin/sh", "-c", "cat << 'EOF' > /build.sh\necho hello\nEOF\n/bin/sh /build.sh &\ntail -f /dev/null\n"},
		},
		{
			name:    "keepalive rejects EOF literal",
			plan:    &DockerRunPlan{Image: "alpine:3.19", Script: "echo EOF", OutputPath: "/out/rebuild"},
			opts:    RunArgsOpts{ContainerName: "b1", OutputMountSrc: "/tmp/b1", KeepAlive: true},
			wantErr: "EOF",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ComposeDockerRunArgs(tt.plan, tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ComposeDockerRunArgs: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("args mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
