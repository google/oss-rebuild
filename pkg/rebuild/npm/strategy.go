// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package npm

import (
	"fmt"
	"time"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type NPMPackBuild struct {
	rebuild.Location
	// NPMVersion is the version of the NPM CLI to use for the build.
	NPMVersion string `json:"npm_version" yaml:"npm_version"`
	// VersionOverride provides an alternative version value to apply to the package.json file.
	VersionOverride string `json:"version_override" yaml:"version_override,omitempty"`
}

var _ rebuild.Strategy = &NPMPackBuild{}

func (b *NPMPackBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{},
		Build: []flow.Step{{
			Uses: "npm/build/pack",
			With: map[string]string{
				"npmVersion":      b.NPMVersion,
				"versionOverride": b.VersionOverride,
			},
		}},
		OutputDir: b.Location.Dir,
	}
}

// GenerateFor generates the instructions for a NPMPackBuild.
func (b *NPMPackBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

// NPMCustomBuild implements a user-specified build script.
type NPMCustomBuild struct {
	rebuild.Location
	NPMVersion        string    `json:"npm_version" yaml:"npm_version"`
	NodeVersion       string    `json:"node_version" yaml:"node_version"`
	VersionOverride   string    `json:"version_override,omitempty" yaml:"version_override,omitempty"`
	Command           string    `json:"command" yaml:"command,omitempty"`
	RegistryTime      time.Time `json:"registry_time" yaml:"registry_time"`
	PrepackRemoveDeps bool      `json:"prepack_remove_deps,omitempty" yaml:"prepack_remove_deps,omitempty"`
	KeepRoot          bool      `json:"keep_root,omitempty" yaml:"keep_root,omitempty"`
}

var _ rebuild.Strategy = &NPMCustomBuild{}

func (b *NPMCustomBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	var registryTime string
	if !b.RegistryTime.IsZero() {
		registryTime = b.RegistryTime.Format(time.RFC3339)
	}
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "npm/deps/custom",
			With: map[string]string{
				"registryTime": registryTime,
				"nodeVersion":  b.NodeVersion,
				"npmVersion":   b.NPMVersion,
			},
		}},
		Build: []flow.Step{{
			Uses: "npm/build/custom",
			With: map[string]string{
				"npmVersion":      b.NPMVersion,
				"versionOverride": b.VersionOverride,
				"keepRoot":        fmt.Sprintf("%t", b.KeepRoot),
				"removeDeps":      fmt.Sprintf("%t", b.PrepackRemoveDeps),
				"command":         b.Command,
			},
		}},
		OutputDir: b.Location.Dir,
	}
}

// GenerateFor generates the instructions for a NPMCustomBuild.
func (b *NPMCustomBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

// Base tools for individual operations
var toolkit = []*flow.Tool{
	{
		Name: "npm/version-override",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if ne .With.version "" -}}
				{{- /* NOTE: Prefer builtin npm for 'npm version' as it wasn't introduced until NPM v6. */ -}}
				PATH=/usr/bin:/bin:/usr/local/bin npm version --prefix {{.With.dir}} --no-git-tag-version {{.With.version}}
				{{- end -}}`)[1:],
			Needs: []string{"npm"},
		}},
	},
	{
		Name: "npm/npx",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{.With.locator}}npx --package=npm@{{.With.npmVersion}} -c '
						{{- if and (ne .With.dir ".") (ne .With.dir "")}}cd {{.With.dir}} && {{end -}}
						{{.With.command}}'`)[1:],
			Needs: []string{"npm"},
		}},
	},
	{
		Name: "npm/setup-registry",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				/usr/bin/npm config --location-global set registry {{.BuildEnv.TimewarpURLFromString "npm" .With.registryTime}}
				trap '/usr/bin/npm config --location-global delete registry' EXIT`)[1:],
			Needs: []string{"npm"},
		}},
	},
	{
		Name: "npm/install-node",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				wget -O - https://unofficial-builds.nodejs.org/download/release/v{{.With.nodeVersion}}/node-v{{.With.nodeVersion}}-linux-x64-musl.tar.gz | tar xzf - --strip-components=1 -C /usr/local/`)[1:],
			Needs: []string{},
		}},
	},
	{
		Name: "npm/install",
		Steps: []flow.Step{
			{
				Uses: "npm/npx",
				With: map[string]string{
					"command": `
						{{- if ne .With.registryTime ""}}npm_config_registry={{.BuildEnv.TimewarpURLFromString "npm" .With.registryTime}} {{end -}}
						npm install --force --no-audit`,
					"npmVersion": "{{.With.npmVersion}}",
					"dir":        "{{.Location.Dir}}",
					"locator":    "{{.With.locator}}",
				},
			},
		},
	},

	// Composite tools for common dependency setups
	{
		Name: "npm/deps/custom",
		Steps: []flow.Step{
			{
				Uses: "npm/install-node",
				With: map[string]string{
					"nodeVersion": "{{.With.nodeVersion}}",
				},
			},
			{
				Uses: "npm/install",
				With: map[string]string{
					"npmVersion":   "{{.With.npmVersion}}",
					"registryTime": "{{.With.registryTime}}",
					"locator":      "/usr/local/bin/",
				},
			},
		},
	},

	// Composite tools for common build patterns
	{
		Name: "npm/build/pack",
		Steps: []flow.Step{
			{
				Uses: "npm/version-override",
				With: map[string]string{
					"version": "{{.With.versionOverride}}",
					"dir":     "{{.Location.Dir}}",
				},
			},
			{
				Uses: "npm/npx",
				With: map[string]string{
					"command":    "npm pack",
					"npmVersion": "{{.With.npmVersion}}",
					"dir":        "{{.Location.Dir}}",
					"locator":    "/usr/bin/",
				},
			},
		},
	},
	{
		Name: "npm/build/custom",
		Steps: []flow.Step{
			{
				Uses: "npm/version-override",
				With: map[string]string{
					"version": "{{.With.versionOverride}}",
					"dir":     "{{.Location.Dir}}",
				},
			},
			{
				Uses: "npm/npx",
				With: map[string]string{
					"command": `
						{{- if eq .With.keepRoot "true"}}npm config set unsafe-perm true && {{end -}}
						{{- if ne .With.command ""}}npm run {{.With.command}} && {{end -}}
						{{- if eq .With.removeDeps "true"}}rm -rf node_modules && {{end -}}
						npm pack`,
					"npmVersion": "{{.With.npmVersion}}",
					"dir":        "{{.Location.Dir}}",
					"locator":    "/usr/local/bin/",
				},
			},
		},
	},
}
