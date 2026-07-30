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

// dockerRunScriptArgs holds template arguments for the phase scripts
type dockerRunScriptArgs struct {
	Inst           rebuild.Instructions
	OS             build.OS
	PackageManager build.PackageManagerCommands
	UseTimewarp    bool
	TimewarpURL    string
	TimewarpAuth   bool
}

// dockerRunPhaseTpls generate the per-phase scripts. Phase boundaries match
// the docker build variants' layers. Scripts are cwd-independent (absolute
// timewarp path, each phase re-enters /src) so they compose into a single
// shell via CombinedScript without semantic drift.
var dockerRunPhaseTpls = template.Must(
	template.New("docker run phases").Funcs(template.FuncMap{
		"list": func(items ...string) []string { return items },
	}).Parse(
		textwrap.Dedent(`
			{{- define "setup" -}}
			{{- if .UseTimewarp}}
			{{- if eq .OS "alpine"}}
			{{.PackageManager.InstallCommand (list "curl")}}
			{{- else}}
			{{.PackageManager.UpdateCmd}}
			{{.PackageManager.InstallCommand (list "curl" "netcat-openbsd")}}
			{{- end}}
			curl {{if .TimewarpAuth}}-H "$AUTH_HEADER" {{end}}{{.TimewarpURL}} > /timewarp
			chmod +x /timewarp
			{{- end}}
			{{.PackageManager.UpdateCmd}}
			{{.PackageManager.InstallCommand .Inst.Requires.SystemDeps}}
			{{- end}}
			{{- define "source" -}}
			mkdir -p /src && cd /src
			{{.Inst.Source}}
			{{- end}}
			{{- define "deps" -}}
			{{- if .UseTimewarp}}
			/timewarp -port 8081 &
			while ! nc -z localhost 8081;do sleep 1;done
			{{- end}}
			cd /src
			{{.Inst.Deps}}
			{{- end}}
			{{- define "build" -}}
			cd /src
			{{.Inst.Build}}
			chmod 444 /src/{{.Inst.OutputPath}}
			cp /src/{{.Inst.OutputPath}} /out/rebuild
			{{- end -}}
			`),
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
		TimewarpHost: "localhost:8081",
		HasRepo:      false,
	}
	instructions, err := input.Strategy.GenerateFor(input.Target, buildEnv)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate rebuild instructions")
	}
	image := opts.Resources.BaseImageConfig.SelectFor(input)
	os := build.DetectOS(image)
	timewarpURL, timewarpAuth, err := p.getToolURL(build.TimewarpTool, opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get timewarp URL")
	}
	args := dockerRunScriptArgs{
		Inst:           instructions,
		OS:             os,
		PackageManager: build.GetPackageManagerCommands(os),
		UseTimewarp:    opts.UseTimewarp,
		TimewarpURL:    timewarpURL,
		TimewarpAuth:   timewarpAuth,
	}
	phase := func(name rebuild.BuildPhase) (string, error) {
		var buf strings.Builder
		if err := dockerRunPhaseTpls.ExecuteTemplate(&buf, string(name), args); err != nil {
			return "", errors.Wrapf(err, "executing %s phase template", name)
		}
		// Conditional first lines leave a leading newline in the render.
		return strings.TrimSpace(buf.String()), nil
	}
	plan := &DockerRunPlan{
		Image:        image,
		WorkingDir:   "/workspace",
		OutputPath:   "/out/rebuild",
		RequiresAuth: len(opts.Resources.ToolAuthRequired) > 0,
		Privileged:   instructions.Requires.Privileged,
	}
	if plan.Setup, err = phase(rebuild.PhaseSetup); err != nil {
		return nil, err
	}
	if plan.Source, err = phase(rebuild.PhaseSource); err != nil {
		return nil, err
	}
	// No deps instructions means no deps phase. Timewarp then never starts,
	// matching the docker build variants' conditional deps layer.
	if instructions.Deps != "" {
		if plan.Deps, err = phase(rebuild.PhaseDeps); err != nil {
			return nil, err
		}
	}
	if plan.Build, err = phase(rebuild.PhaseBuild); err != nil {
		return nil, err
	}
	return plan, nil
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
