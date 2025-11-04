// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"fmt"
	"strings"

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
	RegistryCommit   string            `json:"registry_commit,omitempty" yaml:"registry_commit,omitempty"`
	PackageNames     []string          `json:"package_names,omitempty" yaml:"package_names,omitempty"`
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
			{
				Uses: "cargo/setup-registry",
				With: map[string]string{
					"registryCommit": b.RegistryCommit,
					"packageNames":   strings.Join(b.PackageNames, ","),
					"useGitIndex":    fmt.Sprintf("%t", len(b.PackageNames) > 0),
				},
			},
		},
		Build: []flow.Step{{
			Uses: "cargo/build/package",
			With: map[string]string{
				"dir":                    b.Location.Dir,
				"rustVersion":            b.RustVersion,
				"registryCommit":         b.RegistryCommit,
				"preferPreciseToolchain": "{{.BuildEnv.PreferPreciseToolchain}}",
				"useGitIndex":            fmt.Sprintf("%t", len(b.PackageNames) > 0),
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
				{{- end -}}`[1:]),
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
				{{- end -}}`[1:]),
			Needs: []string{"rustup"},
		}},
	},
	{
		Name: "cargo/setup-registry",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if and (eq .With.useGitIndex "true") (ne .TimewarpHost "") (ne .With.registryCommit "") -}}
				mkdir -p /cargo-index
				wget -O - --header "X-Package-Names: {{.With.packageNames}}" "{{.BuildEnv.TimewarpURLFromString "cargogitarchive" .With.registryCommit}}index.git.tar" | tar -xf - -C /cargo-index
				mkdir -p /.cargo
				printf '[source.crates-io]\nreplace-with = "timewarp-local"\n[source.timewarp-local]\nregistry = "file:///cargo-index"\n' > /.cargo/config.toml
				{{- else if and (ne .TimewarpHost "") (ne .With.registryCommit "") -}}
				mkdir -p /.cargo
				printf '[source.crates-io]\nreplace-with = "timewarp"\n[source.timewarp]\nregistry = "{{.BuildEnv.TimewarpURLFromString "cargosparse" .With.registryCommit}}"\n' > /.cargo/config.toml
				{{- else -}}
				# NOTE: Using current crates.io registry
				{{- end -}}`[1:]),
		}},
	},
	{
		Name: "cargo/build/package",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if and (ne .Location.Dir ".") (ne .Location.Dir "")}}(cd {{.With.dir}} && {{end -}}
				/root/.cargo/bin/cargo package --no-verify
				{{- if and (ne .Location.Dir ".") (ne .Location.Dir "")}}){{end}}`[1:]),
			Needs: []string{"rustup"},
		}},
	},
}
