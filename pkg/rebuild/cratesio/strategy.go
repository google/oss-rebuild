// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
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
	dir := b.Location.Dir
	if dir == "" {
		dir = "."
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
					"rustVersion": b.RustVersion,
				},
			},
			{
				Uses: "cargo/setup-registry",
				With: map[string]string{
					"registryCommit": b.RegistryCommit,
					"packageNames":   strings.Join(b.PackageNames, ","),
					"dir":            dir,
				},
			},
		},
		Build: []flow.Step{{
			Uses: "cargo/build/package",
			With: map[string]string{
				"dir":            b.Location.Dir,
				"rustVersion":    b.RustVersion,
				"registryCommit": b.RegistryCommit,
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
			Runs: textwrap.Dedent(`
				/usr/bin/rustup-init -y --profile minimal --default-toolchain {{.With.rustVersion}}`)[1:],
			Needs: []string{"rustup"},
		}},
	},
	{
		Name: "cargo/setup-registry",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if and (ne .TimewarpHost "") (ne .With.registryCommit "") -}}
				{{if eq .With.packageNames "" -}}
				mkdir -p /.cargo
				printf '[source.crates-io]\nreplace-with = "timewarp"\n[source.timewarp]\nregistry = "{{.BuildEnv.TimewarpURLFromString "cargosparse" .With.registryCommit}}"\n' > /.cargo/config.toml
				{{- else -}}
				# rust-toolchain files can override the strategy's default toolchain.
				cargo_minor="$(cd {{.With.dir}} && /root/.cargo/bin/cargo --version | sed -n 's/^cargo 1\.\([0-9][0-9]*\)\..*/\1/p')"
				if [ -z "$cargo_minor" ]; then
				  echo "Unable to determine Cargo minor version" >&2
				  exit 1
				fi
				if [ "$cargo_minor" -lt 68 ]; then
				mkdir -p /cargo-index
				wget -O - --header "X-Package-Names: {{.With.packageNames}}" "{{.BuildEnv.TimewarpURLFromString "cargogitarchive" .With.registryCommit}}index.git.tar" | tar -xf - -C /cargo-index
				mkdir -p /.cargo
				printf '[source.crates-io]\nreplace-with = "timewarp-local"\n[source.timewarp-local]\nregistry = "file:///cargo-index"\n' > /.cargo/config.toml
				else
				mkdir -p /.cargo
				printf '[source.crates-io]\nreplace-with = "timewarp"\n[source.timewarp]\nregistry = "{{.BuildEnv.TimewarpURLFromString "cargosparse" .With.registryCommit}}"\n' > /.cargo/config.toml
				fi
				{{- end -}}
				{{- else -}}
				# NOTE: Using current crates.io registry
				{{- end -}}`)[1:],
		}},
	},
	{
		Name: "cargo/build/package",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if and (ne .Location.Dir ".") (ne .Location.Dir "")}}(cd {{.With.dir}} && {{end -}}
				/root/.cargo/bin/cargo package --no-verify
				{{- if and (ne .Location.Dir ".") (ne .Location.Dir "")}}){{end}}`)[1:],
			Needs: []string{"rustup"},
		}},
	},
}
