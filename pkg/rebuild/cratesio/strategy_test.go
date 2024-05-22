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
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/go-cmp/cmp"
)

func TestCratesIOCargoPackage(t *testing.T) {

	defaultLocation := rebuild.Location{
		Dir:  "the_dir",
		Ref:  "the_ref",
		Repo: "the_repo",
	}
	tests := []struct {
		name     string
		strategy rebuild.Strategy
		env      rebuild.BuildEnv
		want     rebuild.Instructions
	}{
		{
			"NoExplicitLockfile",
			&CratesIOCargoPackage{
				Location:    defaultLocation,
				RustVersion: "1.77.0",
			},
			rebuild.BuildEnv{HasRepo: true},
			rebuild.Instructions{
				Location:   defaultLocation,
				Source:     "git checkout --force 'the_ref'",
				Deps:       "",
				Build:      `/root/.cargo/bin/cargo package --no-verify --package "path+file://$(readlink -f the_dir)"`,
				SystemDeps: []string{"git", "rustup"},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"ExplicitLockfile",
			&CratesIOCargoPackage{
				Location:    defaultLocation,
				RustVersion: "1.77.0",
				ExplicitLockfile: &ExplicitLockfile{
					LockfileBase64: "lock_base64",
				},
			},
			rebuild.BuildEnv{HasRepo: true},
			rebuild.Instructions{
				Location:   defaultLocation,
				Source:     "git checkout --force 'the_ref'",
				Deps:       "echo 'lock_base64' | base64 -d > Cargo.lock\n",
				Build:      `/root/.cargo/bin/cargo package --no-verify --package "path+file://$(readlink -f the_dir)"`,
				SystemDeps: []string{"git", "rustup"},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"NoLockfilePreciseToolchain",
			&CratesIOCargoPackage{
				Location: rebuild.Location{
					Dir:  "the_dir",
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				RustVersion: "1.77.0",
			},
			rebuild.BuildEnv{HasRepo: true, PreferPreciseToolchain: true},
			rebuild.Instructions{
				Location:   defaultLocation,
				Source:     "git checkout --force 'the_ref'",
				Deps:       "/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0\n",
				Build:      `/root/.cargo/bin/cargo package --no-verify --package "path+file://$(readlink -f the_dir)"`,
				SystemDeps: []string{"git", "rustup"},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"ExplicitLockfilePreciseToolchain",
			&CratesIOCargoPackage{
				Location:    defaultLocation,
				RustVersion: "1.77.0",
				ExplicitLockfile: &ExplicitLockfile{
					LockfileBase64: "lock_base64",
				},
			},
			rebuild.BuildEnv{HasRepo: true, PreferPreciseToolchain: true},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `echo 'lock_base64' | base64 -d > Cargo.lock
/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
`,
				Build:      `/root/.cargo/bin/cargo package --no-verify --package "path+file://$(readlink -f the_dir)"`,
				SystemDeps: []string{"git", "rustup"},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"OldToolchain",
			&CratesIOCargoPackage{
				Location:    defaultLocation,
				RustVersion: "1.55.0",
			},
			rebuild.BuildEnv{HasRepo: true, PreferPreciseToolchain: true},
			rebuild.Instructions{
				Location:   defaultLocation,
				Source:     "git checkout --force 'the_ref'",
				Deps:       "/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.55.0\n",
				Build:      `/root/.cargo/bin/cargo package --no-verify`,
				SystemDeps: []string{"git", "rustup"},
				OutputPath: "target/package/the_artifact",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := tc.strategy.GenerateFor(rebuild.Target{Ecosystem: rebuild.CratesIO, Package: "the_package", Version: "the_version", Artifact: "the_artifact"}, tc.env)
			if err != nil {
				t.Fatalf("Strategy%v.GenerateFor() failed unexpectedly: %v", tc.strategy, err)
			}
			if diff := cmp.Diff(inst, tc.want); diff != "" {
				t.Errorf("Strategy%v.GenerateFor() returned diff (-got +want):\n%s", tc.strategy, diff)
			}
		})
	}
}
