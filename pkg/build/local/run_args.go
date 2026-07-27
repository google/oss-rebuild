// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"fmt"
	"path"
)

// AuthMode selects how the AUTH_HEADER variable reaches the container.
type AuthMode int

const (
	// AuthNone passes no auth header.
	AuthNone AuthMode = iota
	// AuthInline inlines the header value into the argv
	// (-e AUTH_HEADER=<value>). Suitable only when the argv is ephemeral.
	AuthInline
	// AuthEnvPassthrough passes the variable name only (-e AUTH_HEADER),
	// inheriting the value from the docker CLI process environment. Used by
	// backends whose argv is persisted and must not carry secrets.
	AuthEnvPassthrough
)

// RunArgsOpts parameterizes the docker argv compositions.
type RunArgsOpts struct {
	// ContainerName is the container's --name.
	ContainerName string
	// OutputMountSrc is the docker-host directory mounted at the plan's
	// output directory.
	OutputMountSrc string
	// Remove adds --rm: the daemon removes the container once it exits. In
	// the start/exec composition the container only exits when the caller
	// stops its idle init.
	Remove bool
	// AllowPrivileged permits plans that request privileged execution to
	// receive --privileged and the docker socket mount. Callers gate this on
	// their own policy and are responsible for surfacing a refusal.
	AllowPrivileged bool
	// MemoryLimit sets --memory when non-empty.
	MemoryLimit string
	// AuthMode selects how AUTH_HEADER is delivered. AuthValue is its value
	// for AuthInline.
	AuthMode  AuthMode
	AuthValue string
}

// composeContainerArgs assembles the flags shared by both compositions:
// identity, mounts, isolation, and env.
func composeContainerArgs(plan *DockerRunPlan, opts RunArgsOpts) []string {
	args := []string{"--name", opts.ContainerName}
	args = append(args, "-v", fmt.Sprintf("%s:%s", opts.OutputMountSrc, path.Dir(plan.OutputPath)))
	if plan.WorkingDir != "" {
		args = append(args, "-w", plan.WorkingDir)
	}
	if plan.Privileged && opts.AllowPrivileged {
		args = append(args, "--privileged")
		args = append(args, "-v", "/var/run/docker.sock:/var/run/docker.sock")
	}
	if opts.MemoryLimit != "" {
		args = append(args, "--memory", opts.MemoryLimit)
	}
	switch opts.AuthMode {
	case AuthInline:
		args = append(args, "-e", fmt.Sprintf("AUTH_HEADER=%s", opts.AuthValue))
	case AuthEnvPassthrough:
		args = append(args, "-e", "AUTH_HEADER")
	}
	// Disable core dumps
	args = append(args, "--ulimit", "core=0")
	return args
}

// ComposeDockerStartArgs builds the `docker run` argv that starts the build
// container idle. Phases then execute in it via ComposeDockerExecArgs. The
// returned argv excludes the docker command itself.
func ComposeDockerStartArgs(plan *DockerRunPlan, opts RunArgsOpts) []string {
	args := []string{"run", "--detach"}
	if opts.Remove {
		args = append(args, "--rm")
	}
	args = append(args, composeContainerArgs(plan, opts)...)
	return append(args, plan.Image, "sleep", "infinity")
}

// ComposeDockerExecArgs builds the `docker exec` argv running one phase in a
// container started via ComposeDockerStartArgs. Container-level env is
// inherited by every exec.
func ComposeDockerExecArgs(container string, phase Phase) []string {
	return []string{"exec", container, "/bin/sh", "-c", strictPrelude + "\n" + phase.Script}
}
