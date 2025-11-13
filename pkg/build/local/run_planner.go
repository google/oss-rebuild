// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package local

import (
	"context"
	"strings"
	"text/template"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/build"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// DockerRunPlanner generates Docker run execution plans using existing images
type DockerRunPlanner struct {
}

// dockerRunScriptArgs holds template arguments for the run script
type dockerRunScriptArgs struct {
	Inst           rebuild.Instructions
	PackageManager build.PackageManagerCommands
	UseTimewarp    bool
	TimewarpURL    string
	TimewarpAuth   bool
}

// dockerRunScriptTpl is the template for the script executed in the container
var dockerRunScriptTpl = template.Must(
	template.New("docker run script").Funcs(template.FuncMap{
		"indent": func(s string) string { return strings.ReplaceAll(s, "\n", "\n ") },
		"join":   func(sep string, s []string) string { return strings.Join(s, sep) },
		"list":   func(items ...string) []string { return items },
	}).Parse(
		textwrap.Dedent(`
			set -eux
			{{.PackageManager.UpdateCmd}}
			{{- if .UseTimewarp}}
			{{.PackageManager.InstallCommand (list "curl")}}
			curl {{if .TimewarpAuth}}-H "$AUTH_HEADER" {{end}} {{.TimewarpURL}} > timewarp
			chmod +x timewarp
			./timewarp -port 8081 &
			while ! nc -z localhost 8081;do sleep 1;done
			{{- end}}
			mkdir /src && cd /src
			{{.PackageManager.InstallCommand .Inst.Requires.SystemDeps}}
			{{.Inst.Source}}
			{{.Inst.Deps}}
			{{.Inst.Build}}
			cp /src/{{.Inst.OutputPath}} /out/rebuild`)[1:],
	),
)

// NewDockerRunPlanner creates a new Docker run planner
func NewDockerRunPlanner() *DockerRunPlanner {
	return &DockerRunPlanner{}
}

// GeneratePlan implements Planner[*DockerRunPlan]
func (p *DockerRunPlanner) GeneratePlan(ctx context.Context, input rebuild.Input, opts build.PlanOptions) (*DockerRunPlan, error) {
	if opts.UseSyscallMonitor {
		return nil, errors.New("syscall monitor support not implemented")
	}
	buildEnv := rebuild.BuildEnv{
		TimewarpHost:           "localhost:8081",
		HasRepo:                false,
		PreferPreciseToolchain: opts.PreferPreciseToolchain,
	}
	instructions, err := input.Strategy.GenerateFor(input.Target, buildEnv)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate rebuild instructions")
	}
	image := opts.Resources.BaseImageConfig.SelectFor(input)
	script, err := p.generateScript(instructions, input, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate command")
	}
	// Check if any auth is required for this plan
	requiresAuth := len(opts.Resources.ToolAuthRequired) > 0

	return &DockerRunPlan{
		Image:        image,
		Script:       script,
		WorkingDir:   "/workspace",
		OutputPath:   "/out/rebuild",
		RequiresAuth: requiresAuth,
		Privileged:   instructions.Requires.Privileged,
	}, nil
}

func (p *DockerRunPlanner) generateScript(instructions rebuild.Instructions, input rebuild.Input, opts build.PlanOptions) (string, error) {
	// Select base image and detect OS for package management
	baseImage := opts.Resources.BaseImageConfig.SelectFor(input)
	os := build.DetectOS(baseImage)
	pkgMgr := build.GetPackageManagerCommands(os)
	// Extract tool URLs and auth requirements
	timewarpURL, timewarpAuth, err := p.getToolURL(build.TimewarpTool, opts)
	if err != nil {
		return "", errors.Wrap(err, "failed to get timewarp URL")
	}
	// Execute template to generate script
	var buf strings.Builder
	if err := dockerRunScriptTpl.Execute(&buf, dockerRunScriptArgs{
		Inst:           instructions,
		PackageManager: pkgMgr,
		UseTimewarp:    opts.UseTimewarp,
		TimewarpURL:    timewarpURL,
		TimewarpAuth:   timewarpAuth,
	}); err != nil {
		return "", errors.Wrap(err, "executing run script template")
	}
	return buf.String(), nil
}

func (p *DockerRunPlanner) getToolURL(toolType build.ToolType, opts build.PlanOptions) (toolURL string, needsAuth bool, err error) {
	originalURL, exists := opts.Resources.ToolURLs[toolType]
	if !exists {
		return "", false, nil
	}
	// Convert URL and determine auth requirements
	convertedURL, err := build.ConvertURLForRuntime(originalURL)
	if err != nil {
		return "", false, errors.Wrapf(err, "failed to convert URL for %s", toolType)
	}
	return convertedURL, build.NeedsAuth(originalURL, opts.Resources.ToolAuthRequired), nil
}
