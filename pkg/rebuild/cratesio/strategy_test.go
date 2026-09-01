// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package cratesio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestCratesIOCargoPackageQuotesDirectory(t *testing.T) {
	for _, dir := range []string{"package with spaces", "package's source"} {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			want := filepath.Join(root, dir)
			if err := os.MkdirAll(want, 0o755); err != nil {
				t.Fatal(err)
			}
			strategy := &CratesIOCargoPackage{
				Location:    rebuild.Location{Dir: dir},
				RustVersion: "1.77.0",
			}
			if got := strategy.ToWorkflow().Build[0].With["dir"]; got != dir {
				t.Fatalf("dir = %q, want raw value %q", got, dir)
			}
			instructions, err := strategy.GenerateFor(rebuild.Target{Artifact: "test.crate"}, rebuild.BuildEnv{HasRepo: true})
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(instructions.Build, "/root/.cargo/bin/cargo package --no-verify", "pwd", 1)
			cmd := exec.Command("sh", "-c", script)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("build script failed: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != want {
				t.Fatalf("pwd = %q, want %q", got, want)
			}
		})
	}
}

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
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
# NOTE: Using current crates.io registry`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			"ExcludeLockfile",
			&CratesIOCargoPackage{
				Location:        defaultLocation,
				RustVersion:     "1.87.0",
				ExcludeLockfile: true,
			},
			rebuild.BuildEnv{HasRepo: true},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.87.0
# NOTE: Using current crates.io registry`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify --exclude-lockfile)`,
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
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
# NOTE: Using current crates.io registry`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
/root/.cargo/bin/cargo package --no-verify`,
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
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
mkdir -p /.cargo
printf '[source.crates-io]\nreplace-with = "timewarp"\n[source.timewarp]\nregistry = "sparse+http://cargosparse:abc1234@localhost:8081/"\n' > /.cargo/config.toml`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
/root/.cargo/bin/cargo package --no-verify`,
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
/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
# NOTE: Using current crates.io registry`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify)`,
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
			rebuild.BuildEnv{HasRepo: true},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
# NOTE: Using current crates.io registry`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify)`,
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
			rebuild.BuildEnv{HasRepo: true},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `echo 'lock_base64' | base64 -d > Cargo.lock
/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
# NOTE: Using current crates.io registry`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify)`,
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
			rebuild.BuildEnv{HasRepo: true},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.55.0
# NOTE: Using current crates.io registry`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify)`,
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
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.55.0
mkdir -p /cargo-index
wget -O - --header "X-Package-Names: serde,tokio" "http://cargogitarchive:abc1234@localhost:8081/index.git.tar" | tar -xf - -C /cargo-index
mkdir -p /.cargo
printf '[source.crates-io]\nreplace-with = "timewarp-local"\n[source.timewarp-local]\nregistry = "file:///cargo-index"\n' > /.cargo/config.toml`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify)`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "rustup"},
				},
				OutputPath: "target/package/the_artifact",
			},
		},
		{
			// NOTE: Cargo reads config.toml only from 1.39.
			"GitIndexRegistryLegacyConfigName",
			&CratesIOCargoPackage{
				Location:       defaultLocation,
				RustVersion:    "1.35.0",
				RegistryCommit: "abc1234",
				PackageNames:   []string{"serde", "tokio"},
			},
			rebuild.BuildEnv{HasRepo: true, TimewarpHost: "localhost:8081"},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.35.0
mkdir -p /cargo-index
wget -O - --header "X-Package-Names: serde,tokio" "http://cargogitarchive:abc1234@localhost:8081/index.git.tar" | tar -xf - -C /cargo-index
mkdir -p /.cargo
printf '[source.crates-io]\nreplace-with = "timewarp-local"\n[source.timewarp-local]\nregistry = "file:///cargo-index"\n' > /.cargo/config`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify)`,
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
				Deps: `/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0
mkdir -p /.cargo
printf '[source.crates-io]\nreplace-with = "timewarp"\n[source.timewarp]\nregistry = "sparse+http://cargosparse:abc1234@localhost:8081/"\n' > /.cargo/config.toml`,
				Build: `export CARGO_TARGET_DIR="$PWD/target"
(cd 'the_dir' && /root/.cargo/bin/cargo package --no-verify)`,
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
