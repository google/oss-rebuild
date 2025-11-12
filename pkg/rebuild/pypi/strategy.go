// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"time"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// PureWheelBuild aggregates the options controlling a wheel build.
type PureWheelBuild struct {
	rebuild.Location
	Requirements []string  `json:"requirements"`
	RegistryTime time.Time `json:"registry_time" yaml:"registry_time,omitempty"`
}

var _ rebuild.Strategy = &PureWheelBuild{}

func (b *PureWheelBuild) ToWorkflow() *rebuild.WorkflowStrategy {
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
			Uses: "pypi/deps/basic",
			With: map[string]string{
				"registryTime": registryTime,
				"requirements": flow.MustToJSON(b.Requirements),
				"venv":         "/deps",
			},
		}},
		Build: []flow.Step{{
			Uses: "pypi/build/wheel",
			With: map[string]string{
				"dir":     b.Location.Dir,
				"locator": "/deps/bin/",
			},
		}},
		OutputDir: func() string {
			if b.Location.Dir != "" {
				return b.Location.Dir + "/dist"
			}
			return "dist"
		}(),
	}
}

// GenerateFor generates the instructions for a PureWheelBuild.
func (b *PureWheelBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
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
		Name: "pypi/setup-venv",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{.With.locator}}python3 -m venv {{.With.path}}`)[1:],
			Needs: []string{"python3"},
		}},
	},
	{
		Name: "pypi/setup-registry",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if ne .With.registryTime "" -}}
				export PIP_INDEX_URL={{.BuildEnv.TimewarpURLFromString "pypi" .With.registryTime}}
				{{- end -}}`)[1:],
			Needs: []string{},
		}},
	},
	{
		Name: "pypi/install-deps",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{.With.locator}}pip install build
				{{- range $req := .With.requirements | fromJSON}}
				{{$.With.locator}}pip install '{{regexReplace $req "'" "'\\''"}}'{{end}}`)[1:],
			Needs: []string{"python3"},
		}},
	},

	// Composite tools for common workflow steps
	{
		Name: "pypi/deps/basic",
		Steps: []flow.Step{
			{
				Uses: "pypi/setup-venv",
				With: map[string]string{
					"locator": "/usr/bin/",
					"path":    "{{.With.venv}}",
				},
			},
			{
				Uses: "pypi/setup-registry",
				With: map[string]string{
					"registryTime": "{{.With.registryTime}}",
				},
			},
			{
				Uses: "pypi/install-deps",
				With: map[string]string{
					"requirements": "{{.With.requirements}}",
					"locator":      "{{.With.venv}}/bin/",
				},
			},
		},
	},
	{
		Name: "pypi/build/wheel",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{.With.locator}}python3 -m build --wheel -n{{if and (ne .With.dir ".") (ne .With.dir "")}} {{.With.dir}}{{end}}`)[1:],
			Needs: []string{"python3"},
		}},
	},
}
