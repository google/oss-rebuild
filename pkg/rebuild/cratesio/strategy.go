// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// ExplicitLockfile aggregates the options controlling a cargo build of a crate.
type ExplicitLockfile struct {
	LockfileBase64 string `json:"lockfile_base64" yaml:"lockfile_base64,omitempty"`
}

// CratesIOCargoPackage aggregates the options controlling a cargo build of a cratesio package.
type CratesIOCargoPackage struct {
	rebuild.Location
	RustVersion      string            `json:"rust_version" yaml:"rust_version,omitempty"`
	ExplicitLockfile *ExplicitLockfile `json:"explicit_lockfile" yaml:"explicit_lockfile,omitempty"`
}

var _ rebuild.Strategy = &CratesIOCargoPackage{}

func (b *CratesIOCargoPackage) ToWorkflow() *rebuild.WorkflowStrategy {
	lockfile := ""
	if b.ExplicitLockfile != nil {
		lockfile = b.ExplicitLockfile.LockfileBase64
	}
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{
			{
				Uses: "cargo/create-lockfile",
				With: map[string]string{
					"lockfile": lockfile,
				},
			},
			{
				Uses: "cargo/deps/toolchain",
				With: map[string]string{
					"rustVersion":            b.RustVersion,
					"preferPreciseToolchain": "{{.BuildEnv.PreferPreciseToolchain}}",
				},
			},
		},
		Build: []flow.Step{{
			Uses: "cargo/build/package",
			With: map[string]string{
				"dir":                    b.Location.Dir,
				"rustVersion":            b.RustVersion,
				"preferPreciseToolchain": "{{.BuildEnv.PreferPreciseToolchain}}",
			},
		}},
		OutputDir: "target/package",
	}
}

// GenerateFor generates the instructions for a CratesIOCargoPackage.
func (b *CratesIOCargoPackage) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
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
		Name: "cargo/create-lockfile",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if ne .With.lockfile "" -}}
				echo '{{.With.lockfile}}' | base64 -d > Cargo.lock
				{{- end -}}`)[1:],
		}},
	},
	{
		Name: "cargo/deps/toolchain",
		Steps: []flow.Step{{
			// Unless PreferPreciseToolchain is specified, we ignore the exact
			// toolchain version and rely on one that's ambiently installed. This is
			// to avoid using a version that predates sparse index support (>=1.68.0)
			// which requires a full index fetch (~700MB, many minutes of latency).
			Runs: textwrap.Dedent(`
				{{if eq .With.preferPreciseToolchain "true" -}}
				/usr/bin/rustup-init -y --profile minimal --default-toolchain {{.With.rustVersion}}
				{{- end -}}`)[1:],
			Needs: []string{"rustup"},
		}},
	},
	{
		Name: "cargo/build/package",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				/root/.cargo/bin/cargo package --no-verify{{if or (ne .With.preferPreciseToolchain "true") (gt 0 (cmpSemver "1.56.0" .With.rustVersion))}} --package "path+file://$(readlink -f {{.With.dir}})"{{end}}`)[1:],
			Needs: []string{"rustup"},
		}},
	},
}
