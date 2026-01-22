// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestPureWheelBuild(t *testing.T) {
	defaultLocation := rebuild.Location{
		Dir:  "the_dir", // Changed due to directory parsing logic in infer
		Ref:  "the_ref",
		Repo: "the_repo",
	}
	tests := []struct {
		name     string
		strategy rebuild.Strategy
		want     rebuild.Instructions
	}{
		{
			"WithDeps",
			&PureWheelBuild{
				Location:     defaultLocation,
				Requirements: []string{"req_1", "req_2"},
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build
/deps/bin/pip install 'req_1'
/deps/bin/pip install 'req_2'`,
				Build: "/deps/bin/python3 -m build --wheel -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"DepsEscaping",
			&PureWheelBuild{
				Location:     defaultLocation,
				Requirements: []string{"req_1<='1.2.3'"},
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build
/deps/bin/pip install 'req_1<='\''1.2.3'\'''`,
				Build: "/deps/bin/python3 -m build --wheel -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"NoDeps",
			&PureWheelBuild{
				Location: defaultLocation,
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build`,
				Build: "/deps/bin/python3 -m build --wheel -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithTimewarp",
			&PureWheelBuild{
				Location:     defaultLocation,
				RegistryTime: time.Date(2006, time.January, 2, 3, 4, 5, 0, time.UTC),
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
export PIP_INDEX_URL=http://pypi:2006-01-02T03:04:05Z@orange/simple
/deps/bin/pip install build`,
				Build: "/deps/bin/python3 -m build --wheel -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithoutDir",
			&PureWheelBuild{
				Location: rebuild.Location{Ref: "the_ref", Repo: "the_repo"},
			},
			rebuild.Instructions{
				Location: rebuild.Location{Ref: "the_ref", Repo: "the_repo"},
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build`,
				Build: "/deps/bin/python3 -m build --wheel -n",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "dist/the_artifact",
			},
		},
		{
			"WithPythonVersion",
			&PureWheelBuild{
				Location:      defaultLocation,
				PythonVersion: "3.11",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/uvx uv venv /deps --seed --python 3.11
/deps/bin/pip install build`,
				Build: "/deps/bin/python3 -m build --wheel -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := tc.strategy.GenerateFor(rebuild.Target{Ecosystem: rebuild.PyPI, Package: "the_package", Version: "the_version", Artifact: "the_artifact"}, rebuild.BuildEnv{HasRepo: true, TimewarpHost: "orange"})
			if err != nil {
				t.Fatalf("%s: Strategy%v.GenerateFor() failed unexpectedly: %v", tc.name, tc.strategy, err)
			}
			if diff := cmp.Diff(inst, tc.want); diff != "" {
				t.Errorf("Strategy%v.GenerateFor() returned diff (-got +want):\n%s", tc.strategy, diff)
			}
		})
	}
}

func TestSourceDistBuild(t *testing.T) {
	defaultLocation := rebuild.Location{
		Dir:  "the_dir",
		Ref:  "the_ref",
		Repo: "the_repo",
	}
	tests := []struct {
		name     string
		strategy rebuild.Strategy
		want     rebuild.Instructions
	}{
		{
			"WithDeps",
			&PyPISdistBuild{
				Location: defaultLocation,
				Requirements: []string{
					"req_1",
					"req_2",
				},
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build
/deps/bin/pip install 'req_1'
/deps/bin/pip install 'req_2'`,
				Build: "/deps/bin/python3 -m build --sdist -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"DepsEscaping",
			&PyPISdistBuild{
				Location: defaultLocation,
				Requirements: []string{
					"req_1<='1.2.3'",
				},
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build
/deps/bin/pip install 'req_1<='\''1.2.3'\'''`,
				Build: "/deps/bin/python3 -m build --sdist -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"NoDeps",
			&PyPISdistBuild{
				Location: defaultLocation,
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build`,
				Build: "/deps/bin/python3 -m build --sdist -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithTimewarp",
			&PyPISdistBuild{
				Location:     defaultLocation,
				RegistryTime: time.Date(2006, time.January, 2, 3, 4, 5, 0, time.UTC),
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
export PIP_INDEX_URL=http://pypi:2006-01-02T03:04:05Z@orange/simple
/deps/bin/pip install build`,
				Build: "/deps/bin/python3 -m build --sdist -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithoutDir",
			&PyPISdistBuild{
				Location: rebuild.Location{Ref: "the_ref", Repo: "the_repo"},
			},
			rebuild.Instructions{
				Location: rebuild.Location{Ref: "the_ref", Repo: "the_repo"},
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build`,
				Build: "/deps/bin/python3 -m build --sdist -n",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "dist/the_artifact",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := tc.strategy.GenerateFor(rebuild.Target{Ecosystem: rebuild.PyPI, Package: "the_package", Version: "the_version", Artifact: "the_artifact"}, rebuild.BuildEnv{HasRepo: true, TimewarpHost: "orange"})
			if err != nil {
				t.Fatalf("%s: Strategy%v.GenerateFor() failed unexpectedly: %v", tc.name, tc.strategy, err)
			}
			if diff := cmp.Diff(inst, tc.want); diff != "" {
				t.Errorf("Strategy%v.GenerateFor() returned diff (-got +want):\n%s", tc.strategy, diff)
			}
		})
	}
}
