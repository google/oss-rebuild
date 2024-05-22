// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cratesio

import (
	"path"

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

// Generate generates the instructions for a CratesIOCargoPackage
func (b *CratesIOCargoPackage) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	src, err := rebuild.BasicSourceSetup(b.Location, &be)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	// Unless PreferPreciseToolchain is specified, we ignore CargoBuildExplicitLockfile.Version. Too
	// many of these predate sparse index support introduced in 1.68.0. Without this, the full index
	// requires ~700MB and minutes to fetch.
	deps, err := rebuild.PopulateTemplate(`
{{if ne .ExplicitLockfile nil -}}
echo '{{.ExplicitLockfile.LockfileBase64}}' | base64 -d > Cargo.lock
{{end -}}
{{if .BuildEnv.PreferPreciseToolchain -}}
/usr/bin/rustup-init -y --profile minimal --default-toolchain {{.RustVersion}}
{{end -}}
`, struct {
		CratesIOCargoPackage
		BuildEnv rebuild.BuildEnv
	}{*b, be})
	if err != nil {
		return rebuild.Instructions{}, err
	}
	build, err := rebuild.PopulateTemplate(`
/root/.cargo/bin/cargo package --no-verify{{if or (not .BuildEnv.PreferPreciseToolchain) (gt 0 (SemverCmp "1.56.0" .RustVersion))}} --package "path+file://$(readlink -f {{.Location.Dir}})"{{end}}
`, struct {
		CratesIOCargoPackage
		BuildEnv rebuild.BuildEnv
	}{*b, be})
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return rebuild.Instructions{
		Location:   b.Location,
		Source:     src,
		Deps:       deps,
		Build:      build,
		SystemDeps: []string{"git", "rustup"},
		OutputPath: path.Join("target", "package", t.Artifact),
	}, nil
}
