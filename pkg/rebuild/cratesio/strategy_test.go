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
				Location:   defaultLocation,
				Source:     "git checkout --force 'the_ref'",
				Deps:       "",
				Build:      `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
				SystemDeps: []string{"git", "rustup"},
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
				Source:     "git checkout --force 'the_ref'",
				Deps:       "",
				Build:      `/root/.cargo/bin/cargo package --no-verify`,
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
				Deps:       "echo 'lock_base64' | base64 -d > Cargo.lock",
				Build:      `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
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
				Deps:       "/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0",
				Build:      `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
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
/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.77.0`,
				Build:      `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
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
				Deps:       "/usr/bin/rustup-init -y --profile minimal --default-toolchain 1.55.0",
				Build:      `(cd the_dir && /root/.cargo/bin/cargo package --no-verify)`,
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
