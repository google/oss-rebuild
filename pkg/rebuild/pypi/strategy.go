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
	Requirements  []string  `json:"requirements"`
	PythonVersion string    `json:"python_version"`
	RegistryTime  time.Time `json:"registry_time" yaml:"registry_time,omitempty"`
}

type SourceDistributionBuild struct {
	rebuild.Location
	Requirements  []string  `json:"requirements"`
	PythonVersion string    `json:"python_version"`
	RegistryTime  time.Time `json:"registry_time" yaml:"registry_time,omitempty"`
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
				"registryTime":  registryTime,
				"requirements":  flow.MustToJSON(b.Requirements),
				"pythonVersion": b.PythonVersion,
				"venv":          "deps",
			},
		}},
		Build: []flow.Step{{
			Uses: "pypi/build/wheel",
			With: map[string]string{
				"dir":     b.Location.Dir,
				"locator": "deps/bin/",
			},
		}},
		OutputDir: "dist",
	}
}

// GenerateFor generates the instructions for a PureWheelBuild.
func (b *PureWheelBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func (b *SourceDistributionBuild) ToWorkflow() *rebuild.WorkflowStrategy {
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
				"registryTime":  registryTime,
				"requirements":  flow.MustToJSON(b.Requirements),
				"pythonVersion": b.PythonVersion,
				"venv":          "deps",
			},
		}},
		Build: []flow.Step{{
			Uses: "pypi/build/sdist",
			With: map[string]string{
				"dir":     b.Location.Dir,
				"locator": "deps/bin/",
			},
		}},
		OutputDir: "dist",
	}
}

func (b *SourceDistributionBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

// if ! command -v pyenv &> /dev/null; then
//
//		curl https://raw.githubusercontent.com/pyenv/pyenv-installer/master/bin/pyenv-installer | bash
//		export PATH="/root/.pyenv/bin:$PATH"
//		eval "$(pyenv init -)"
//		eval "$(pyenv virtualenv-init -)"
//	fi
//
// /root/.pyenv/bin/pyenv global {{.With.version}}`)[1:],
// "bash", "clang", "curl", "build-base", "patch", "zip", "zlib-dev", "libffi-dev", "linux-headers", "readline-dev", "openssl", "openssl-dev", "sqlite-dev", "bzip2-dev"
// Base tools for individual operations
var toolkit = []*flow.Tool{
	//  Use clang to avoid issues with openssl
	// Python versions before 3.6 cannot be compiled with openssl 3.x, they require 1.0.x
	{
		Name: "pypi/setup-python",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				if [ ! -d "/root/.pyenv" ]; then
					if ! command -v curl &> /dev/null; then
						apk add clang curl build-base patch zip zlib-dev libffi-dev linux-headers readline-dev openssl openssl-dev sqlite-dev bzip2-dev xz-dev
					fi
					curl https://raw.githubusercontent.com/pyenv/pyenv-installer/master/bin/pyenv-installer | bash
				fi
				CC=clang /root/.pyenv/bin/pyenv install -s {{.With.version}}`)[1:],
			Needs: []string{"bash", "clang", "curl", "build-base", "patch", "zip", "zlib-dev", "libffi-dev", "linux-headers", "readline-dev", "openssl", "openssl-dev", "sqlite-dev", "bzip2-dev"},
		}},
	}, {

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
				{{$.With.locator}}pip install {{$req}}{{end}}`)[1:],
			Needs: []string{"python3"},
		}},
	},

	// Composite tools for common workflow steps
	{
		Name: "pypi/deps/basic",
		Steps: []flow.Step{
			{
				Uses: "pypi/setup-python",
				With: map[string]string{
					"version": "{{.With.pythonVersion}}",
				},
			},
			{
				Uses: "pypi/setup-venv",
				With: map[string]string{
					// "locator": "/usr/bin/",
					"locator": "/root/.pyenv/versions/{{.With.pythonVersion}}/bin/",
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
	{
		Name: "pypi/build/sdist",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{.With.locator}}python3 -m build --sdist -n{{if and (ne .With.dir ".") (ne .With.dir "")}} {{.With.dir}}{{end}}`)[1:],
			Needs: []string{"python3"},
		}},
	},
}
