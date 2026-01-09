// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"bytes"
	"context"
	"path"
	"strings"
	"text/template"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// dockerBuildContainerArgs matches the structure used in GCB planner for consistency
type dockerBuildContainerArgs struct {
	rebuild.Instructions
	BaseImage      string
	OS             build.OS
	PackageManager build.PackageManagerCommands
	UseTimewarp    bool
	TimewarpURL    string
	TimewarpAuth   bool
}

// dockerBuildDockerfileTpl generates Dockerfiles for local Docker builds
var dockerBuildDockerfileTpl = template.Must(
	template.New("docker build dockerfile").Funcs(template.FuncMap{
		"indent": func(s string) string { return strings.ReplaceAll(s, "\n", "\n\t") },
		"join":   func(sep string, s []string) string { return strings.Join(s, sep) },
		"list":   func(s string) []string { return []string{s} },
	}).Parse(
		textwrap.Dedent(`
			#syntax=docker/dockerfile:1.10
			FROM {{.BaseImage}}
			RUN {{if .TimewarpAuth}}--mount=type=secret,id=auth_header {{end}}<<-'EOF'
				set -eux
			{{- if .UseTimewarp}}
				{{- $hasCurl := or (eq .OS "debian") (eq .OS "ubuntu")}}
				{{- $hasWget := eq .OS "alpine"}}
				{{- if .TimewarpAuth}}
				{{if not $hasCurl}}{{.PackageManager.InstallCommand (list "curl")}} && {{end}}curl -H @/run/secrets/auth_header
			{{- else if $hasWget}}
				wget -O -
			{{- else if $hasCurl}}
				curl
				{{- end}} {{.TimewarpURL}} > timewarp
				chmod +x timewarp
			{{- end}}
				{{.PackageManager.UpdateCmd}}
				{{.PackageManager.InstallCommand .Instructions.Requires.SystemDeps}}
				EOF
			RUN <<-'EOF'
				set -eux
			{{- if .UseTimewarp}}
				./timewarp -port 8080 &
				while ! nc -z localhost 8080;do sleep 1;done
			{{- end}}
				mkdir -p /src && cd /src
				{{.Instructions.Source | indent}}
				{{.Instructions.Deps | indent}}
				EOF
			COPY --chmod=755 <<-'EOF' /build
				set -eux
				{{.Instructions.Build | indent}}
				chmod 444 /src/{{.Instructions.OutputPath}}
				mkdir -p /out && cp /src/{{.Instructions.OutputPath}} /out/
				EOF
			WORKDIR "/src"
			ENTRYPOINT ["/bin/sh","/build"]
			`)[1:], // remove leading newline
	))

// DockerBuildPlanner generates Docker build execution plans
type DockerBuildPlanner struct{}

// NewDockerBuildPlanner creates a new Docker build planner
func NewDockerBuildPlanner() *DockerBuildPlanner {
	return &DockerBuildPlanner{}
}

// GeneratePlan implements Planner[*DockerBuildPlan]
func (p *DockerBuildPlanner) GeneratePlan(ctx context.Context, input rebuild.Input, opts build.PlanOptions) (*DockerBuildPlan, error) {
	buildEnv := rebuild.BuildEnv{
		TimewarpHost: "localhost:8080",
		HasRepo:      false,
	}
	instructions, err := input.Strategy.GenerateFor(input.Target, buildEnv)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate rebuild instructions")
	}
	dockerfile, err := p.generateDockerfile(input, instructions, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate Dockerfile")
	}
	return &DockerBuildPlan{
		Dockerfile: dockerfile,
		OutputPath: path.Join("/out", path.Base(instructions.OutputPath)),
		Privileged: instructions.Requires.Privileged,
	}, nil
}

// generateDockerfile creates a Dockerfile from rebuild instructions and options using templates
func (p *DockerBuildPlanner) generateDockerfile(input rebuild.Input, instructions rebuild.Instructions, opts build.PlanOptions) (string, error) {
	baseImage := opts.Resources.BaseImageConfig.SelectFor(input)

	// Detect OS and get package manager commands
	os := build.DetectOS(baseImage)
	pkgMgr := build.GetPackageManagerCommands(os)

	// Extract tool URLs and auth requirements
	timewarpURL, timewarpAuth, err := p.getToolURL(build.TimewarpTool, opts)
	if err != nil {
		return "", errors.Wrap(err, "failed to get timewarp URL")
	}
	args := dockerBuildContainerArgs{
		Instructions:   instructions,
		BaseImage:      baseImage,
		OS:             os,
		PackageManager: pkgMgr,
		UseTimewarp:    opts.UseTimewarp,
		TimewarpURL:    timewarpURL,
		TimewarpAuth:   timewarpAuth,
	}

	var buf bytes.Buffer
	if err := dockerBuildDockerfileTpl.Execute(&buf, args); err != nil {
		return "", errors.Wrap(err, "failed to execute Dockerfile template")
	}

	return buf.String(), nil
}

// Helper methods to extract configuration from options (matching GCB planner)
func (p *DockerBuildPlanner) getToolURL(toolType build.ToolType, opts build.PlanOptions) (toolURL string, needsAuth bool, err error) {
	originalURL, exists := opts.Resources.ToolURLs[toolType]
	if !exists {
		return "", false, nil
	}
	convertedURL, err := build.ConvertURLForRuntime(originalURL)
	if err != nil {
		return "", false, errors.Wrapf(err, "failed to convert URL for %s", toolType)
	}
	return convertedURL, build.NeedsAuth(originalURL, opts.Resources.ToolAuthRequired), nil
}
