// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
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
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps:     "# NOTE: Using current crates.io registry",
				Build:    `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"NoDir",
			&CratesIOCargoPackage{
				Location: rebuild.Location{
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				RustVersion: "1.77.0",
			},
			rebuild.BuildEnv{HasRepo: true},
			rebuild.Instructions{
				Location: rebuild.Location{
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				Source: "git checkout --force 'the_ref'",
				Deps:   "# NOTE: Using current crates.io registry",
				Build:  `/root/.cargo/bin/cargo package --no-verify`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"NoDirTimewarp",
			&CratesIOCargoPackage{
				Location: rebuild.Location{
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				RustVersion:    "1.77.0",
				RegistryCommit: "abc1234",
			},
			rebuild.BuildEnv{HasRepo: true, TimewarpHost: "localhost:8081"},
			rebuild.Instructions{
				Location: rebuild.Location{
					Ref:  "the_ref",
					Repo: "the_repo",
				},
				Source: "git checkout --force 'the_ref'",
				Deps: `mkdir -p /.cargo
printf '[source.crates-io]\nreplace-with = "timewarp"\n[source.timewarp]\nregistry = "sparse+http://cargosparse:abc1234@localhost:8081/"\n' > /.cargo/config.toml`,
				Build: `/root/.cargo/bin/cargo package --no-verify`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
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
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `echo 'lock_base64' | base64 -d > Cargo.lock
# NOTE: Using current crates.io registry`,
				Build: `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
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
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
# NOTE: Using current crates.io registry`,
				Build: `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
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
# NOTE: Using current crates.io registry`,
				Build: `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
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
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.55.0
# NOTE: Using current crates.io registry`,
				Build: `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"GitIndexRegistry",
			&CratesIOCargoPackage{
				Location:       defaultLocation,
				RustVersion:    "1.55.0",
				RegistryCommit: "abc1234",
				PackageNames:   []string{"serde", "tokio"},
			},
			rebuild.BuildEnv{HasRepo: true, TimewarpHost: "localhost:8081"},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `mkdir -p /cargo-index
wget -O - --header "X-Package-Names: serde,tokio" "http://cargogitarchive:abc1234@localhost:8081/index.git.tar" | tar -xf - -C /cargo-index
mkdir -p /.cargo
printf '[source.crates-io]\nreplace-with = "timewarp-local"\n[source.timewarp-local]\nregistry = "file:///cargo-index"\n' > /.cargo/config.toml`,
				Build: `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"SparseRegistry",
			&CratesIOCargoPackage{
				Location:       defaultLocation,
				RustVersion:    "1.77.0",
				RegistryCommit: "abc1234",
			},
			rebuild.BuildEnv{HasRepo: true, TimewarpHost: "localhost:8081"},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `mkdir -p /.cargo
printf '[source.crates-io]\nreplace-with = "timewarp"\n[source.timewarp]\nregistry = "sparse+http://cargosparse:abc1234@localhost:8081/"\n' > /.cargo/config.toml`,
				Build: `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
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
