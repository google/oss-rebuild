// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"fmt"
	"path"
	"strings"

	"github.com/pkg/errors"
)

// AuthMode selects how the AUTH_HEADER variable reaches the container in
// ComposeDockerRunArgs.
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

// RunArgsOpts parameterizes ComposeDockerRunArgs. It makes the legitimate
// backend divergences explicit so alternate executors (e.g. the scratch VM
// executor) share a single composition of `docker run` argv with the local
// executor.
type RunArgsOpts struct {
	// ContainerName is the container's --name.
	ContainerName string
	// OutputMountSrc is the docker-host directory mounted at the plan's
	// output directory.
	OutputMountSrc string
	// Remove adds --rm.
	Remove bool
	// AllowPrivileged permits plans that request privileged execution to
	// receive --privileged and the docker socket mount. Callers gate this on
	// their own policy and are responsible for surfacing a refusal.
	AllowPrivileged bool
	// MemoryLimit sets --memory when non-empty.
	MemoryLimit string
	// AuthMode selects how AUTH_HEADER is delivered; AuthValue is its value
	// for AuthInline.
	AuthMode  AuthMode
	AuthValue string
	// KeepAlive wraps the inline script so the container stays up after the
	// build completes (local interactive debugging).
	KeepAlive bool
}

// ComposeDockerRunArgs builds the `docker run` argv for executing plan under
// the given options. The returned argv excludes the docker command itself.
func ComposeDockerRunArgs(plan *DockerRunPlan, opts RunArgsOpts) ([]string, error) {
	args := []string{"run"}
	if opts.Remove {
		args = append(args, "--rm")
	}
	args = append(args, "--name", opts.ContainerName)
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
	args = append(args, plan.Image)
	switch {
	case opts.KeepAlive:
		// To keep the container alive, execute the build script in the
		// background and keep an infinite process in the foreground. The
		// script is written to a file via heredoc, so an EOF literal inside
		// it would corrupt the wrapper.
		if strings.Contains(plan.Script, "EOF") {
			return nil, errors.New("build script contains unexpected 'EOF' literal")
		}
		wrapped := fmt.Sprintf("cat << 'EOF' > /build.sh\n%s\nEOF\n/bin/sh /build.sh &\ntail -f /dev/null\n", plan.Script)
		args = append(args, "/bin/sh", "-c", wrapped)
	default:
		args = append(args, "/bin/sh", "-c", plan.Script)
	}
	return args, nil
}
