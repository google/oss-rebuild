// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"strings"
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
			"WithPythonTag",
			&PureWheelBuild{
				Location:     defaultLocation,
				Requirements: []string{"setuptools<=56.2.0"},
				PythonTag:    "py2.py3",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build
/deps/bin/pip install 'setuptools<=56.2.0'`,
				Build: `printf '[bdist_wheel]\npython-tag = py2.py3\n' >~/.pydistutils.cfg
/deps/bin/python3 -m build --wheel -n the_dir`,
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithUVBackend",
			&PureWheelBuild{
				Location:     defaultLocation,
				Requirements: []string{"uv-build==0.10.0"},
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build
/deps/bin/pip install 'uv-build==0.10.0'`,
				Build: "PATH=/deps/bin/:$PATH /deps/bin/python3 -m build --wheel -n the_dir",
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
/deps/bin/pip install build
export PIP_INDEX_URL=http://pypi:2006-01-02T03:04:05Z@orange/simple`,
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
			&SdistBuild{
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
			&SdistBuild{
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
			&SdistBuild{
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
			&SdistBuild{
				Location:     defaultLocation,
				RegistryTime: time.Date(2006, time.January, 2, 3, 4, 5, 0, time.UTC),
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `/usr/bin/python3 -m venv /deps
/deps/bin/pip install build
export PIP_INDEX_URL=http://pypi:2006-01-02T03:04:05Z@orange/simple`,
				Build: "/deps/bin/python3 -m build --sdist -n the_dir",
				Requires: rebuild.RequiredEnv{
					SystemDeps: []string{"git", "python3", "uv"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithoutDir",
			&SdistBuild{
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

func TestPlatformWheelBuild(t *testing.T) {
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
			&PlatformWheelBuild{
				Location:     defaultLocation,
				PythonTag:    "cp310",
				ABITag:       "cp310",
				Requirements: []string{"req_1", "req_2"},
				PlatformTag:  "manylinux_2_17_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp310-cp310/bin/python /opt/python/cp310-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel
/deps/bin/pip install 'req_1'
/deps/bin/pip install 'req_2'`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"DepsEscaping",
			&PlatformWheelBuild{
				Location:     defaultLocation,
				PythonTag:    "cp310",
				ABITag:       "cp310",
				Requirements: []string{"req_1<='1.2.3'"},
				PlatformTag:  "manylinux_2_17_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp310-cp310/bin/python /opt/python/cp310-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel
/deps/bin/pip install 'req_1<='\''1.2.3'\'''`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"NoDeps",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp310",
				ABITag:      "cp310",
				PlatformTag: "manylinux_2_17_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp310-cp310/bin/python /opt/python/cp310-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"PinnedBaseImage",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp312",
				ABITag:      "cp312",
				PlatformTag: "musllinux_1_2_x86_64",
				BaseImage:   "quay.io/pypa/musllinux_1_2_x86_64:2026.03.20-1@sha256:5b6fe3ed82ff48748c5c528fc82e42674a5363890ed949923af65b0c5b5ef76c",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp312-cp312/bin/python /opt/python/cp312-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat musllinux_1_2_x86_64 -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/musllinux_1_2_x86_64:2026.03.20-1@sha256:5b6fe3ed82ff48748c5c528fc82e42674a5363890ed949923af65b0c5b5ef76c",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"BuildRoot",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp310",
				ABITag:      "cp310",
				PlatformTag: "manylinux_2_17_x86_64",
				BuildRoot:   "/project",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp310-cp310/bin/python /opt/python/cp310-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `mkdir -p $(dirname /project)
mkdir /project
cp -a . /project
/deps/bin/python3 -m build --wheel -n --outdir the_dir/dist /project/the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithTimewarp",
			&PlatformWheelBuild{
				Location:     defaultLocation,
				PlatformTag:  "manylinux_2_17_x86_64",
				RegistryTime: time.Date(2006, time.January, 2, 3, 4, 5, 0, time.UTC),
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp3*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel
export PIP_INDEX_URL=http://pypi:2006-01-02T03:04:05Z@orange/simple`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithoutDir",
			&PlatformWheelBuild{
				Location:    rebuild.Location{Ref: "the_ref", Repo: "the_repo"},
				PlatformTag: "manylinux_2_17_x86_64",
			},
			rebuild.Instructions{
				Location: rebuild.Location{Ref: "the_ref", Repo: "the_repo"},
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp3*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n
mkdir -p dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair dist/*.whl --plat manylinux_2_17_x86_64 --only-plat -w dist/repaired/
rm -f dist/*.whl
mv dist/repaired/*.whl dist/the_artifact
rm -rf dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "dist/the_artifact",
			},
		},
		{
			"PEP600WithLegacyAlias",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp312",
				ABITag:      "cp312",
				PlatformTag: "manylinux_2_17_x86_64.manylinux2014_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp312-cp312/bin/python /opt/python/cp312-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"LegacyOnlyTag",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp310",
				ABITag:      "cp310",
				PlatformTag: "manylinux2014_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp310-cp310/bin/python /opt/python/cp310-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux2014_x86_64 -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"CompressedTagSet",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp310",
				ABITag:      "cp310",
				PlatformTag: "manylinux_2_28_x86_64.manylinux_2_5_x86_64.manylinux1_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp310-cp310/bin/python /opt/python/cp310-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_28_x86_64 -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux_2_28_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"SpecificPythonVersion",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp38",
				ABITag:      "cp38",
				PlatformTag: "manylinux_2_28_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp38-cp38/bin/python /opt/python/cp38-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_28_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux_2_28_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"PythonTagWithoutABITag",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp38",
				PlatformTag: "manylinux_2_28_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp38-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_28_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux_2_28_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"ABI3",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp38",
				ABITag:      "abi3",
				PlatformTag: "manylinux_2_28_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp38-abi3/bin/python /opt/python/cp38-*/bin/python /opt/python/cp3*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_28_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux_2_28_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithSystemDeps",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp310",
				ABITag:      "cp310",
				PlatformTag: "manylinux_2_28_x86_64",
				SystemDeps:  []string{"/usr/bin/dot", "graphviz-devel"},
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp310-cp310/bin/python /opt/python/cp310-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_28_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux_2_28_x86_64",
					SystemDeps: []string{"/usr/bin/dot", "graphviz-devel", "git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithBuildEnvPath",
			&PlatformWheelBuild{
				Location:     defaultLocation,
				PythonTag:    "cp310",
				ABITag:       "cp310",
				Requirements: []string{"numpy==2.0.0"},
				PlatformTag:  "manylinux_2_28_x86_64",
				BuildEnvPath: "/tmp/build-env-v9c0qwj_",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp310-cp310/bin/python /opt/python/cp310-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /tmp/build-env-v9c0qwj_
/tmp/build-env-v9c0qwj_/bin/pip install build wheel
/tmp/build-env-v9c0qwj_/bin/pip install 'numpy==2.0.0'`,
				Build: `/tmp/build-env-v9c0qwj_/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/tmp/build-env-v9c0qwj_/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_28_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux_2_28_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithRust",
			&PlatformWheelBuild{
				Location:     defaultLocation,
				PythonTag:    "cp312",
				ABITag:       "cp312",
				Requirements: []string{"setuptools-rust==1.9.0"},
				PlatformTag:  "manylinux_2_17_x86_64.manylinux2014_x86_64",
				RustVersion:  "1.78.0",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp312-cp312/bin/python /opt/python/cp312-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel
/deps/bin/pip install 'setuptools-rust==1.9.0'
wget -O rustup-init https://static.rust-lang.org/rustup/dist/x86_64-unknown-linux-gnu/rustup-init
chmod +x rustup-init
CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup ./rustup-init -y --profile minimal --default-toolchain 1.78.0 --no-modify-path
rm -f rustup-init`,
				Build: `export CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup RUSTUP_TOOLCHAIN=1.78.0 PATH=/root/.rustup/toolchains/1.78.0-x86_64-unknown-linux-gnu/bin:/root/.cargo/bin:$PATH
/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git", "wget"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithNightlyRust",
			&PlatformWheelBuild{
				Location:     defaultLocation,
				PythonTag:    "cp312",
				ABITag:       "cp312",
				Requirements: []string{"setuptools-rust==1.9.0"},
				PlatformTag:  "manylinux_2_17_x86_64.manylinux2014_x86_64",
				RustVersion:  "nightly-2024-06-01",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp312-cp312/bin/python /opt/python/cp312-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel
/deps/bin/pip install 'setuptools-rust==1.9.0'
wget -O rustup-init https://static.rust-lang.org/rustup/dist/x86_64-unknown-linux-gnu/rustup-init
chmod +x rustup-init
CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup ./rustup-init -y --profile minimal --default-toolchain nightly-2024-06-01 --no-modify-path
rm -f rustup-init`,
				Build: `export CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup RUSTUP_TOOLCHAIN=nightly-2024-06-01 PATH=/root/.rustup/toolchains/nightly-2024-06-01-x86_64-unknown-linux-gnu/bin:/root/.cargo/bin:$PATH
/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git", "wget"},
				},
				OutputPath: "the_dir/dist/the_artifact",
			},
		},
		{
			"WithCargoTarget",
			&PlatformWheelBuild{
				Location:     defaultLocation,
				PythonTag:    "cp312",
				ABITag:       "cp312",
				Requirements: []string{"maturin==1.7.4"},
				PlatformTag:  "musllinux_1_2_x86_64",
				RustVersion:  "1.81.0",
				CargoTarget:  true,
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `for py in /opt/python/cp312-cp312/bin/python /opt/python/cp312-*/bin/python; do [ -x "$py" ] && break; done
"$py" -m venv /deps
/deps/bin/pip install build wheel
/deps/bin/pip install 'maturin==1.7.4'
wget -O rustup-init https://static.rust-lang.org/rustup/dist/x86_64-unknown-linux-musl/rustup-init
chmod +x rustup-init
CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup ./rustup-init -y --profile minimal --default-toolchain 1.81.0 --no-modify-path
rm -f rustup-init`,
				Build: `export CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup RUSTUP_TOOLCHAIN=1.81.0 PATH=/root/.rustup/toolchains/1.81.0-x86_64-unknown-linux-musl/bin:/root/.cargo/bin:$PATH
export MATURIN_PEP517_ARGS='--target x86_64-unknown-linux-musl --compatibility musllinux_1_2'
PATH=/deps/bin/:$PATH /deps/bin/python3 -m build --wheel -n the_dir
[ -e the_dir/dist/the_artifact ] || mv the_dir/dist/*.whl the_dir/dist/the_artifact`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/musllinux_1_2_x86_64",
					SystemDeps: []string{"git", "wget"},
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

func TestPlatformWheelBuildRustErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy *PlatformWheelBuild
		want     string
	}{
		{"FloatingVersion", &PlatformWheelBuild{PlatformTag: "manylinux_2_28_x86_64", RustVersion: "stable"}, "no Rust"},
		{"UndatedNightly", &PlatformWheelBuild{PlatformTag: "manylinux_2_28_x86_64", RustVersion: "nightly"}, "no Rust"},
		{"InjectedVersion", &PlatformWheelBuild{PlatformTag: "manylinux_2_28_x86_64", RustVersion: "1.78.0; rm -rf /"}, "no Rust"},
		{"NoX86_64", &PlatformWheelBuild{PlatformTag: "manylinux_2_28_aarch64", BaseImage: "quay.io/pypa/manylinux_2_28_aarch64", RustVersion: "1.78.0"}, "no Rust"},
		{"CargoTargetWithoutRust", &PlatformWheelBuild{PlatformTag: "manylinux_2_28_x86_64", CargoTarget: true}, "requires a Rust version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.strategy.ToWorkflow(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ToWorkflow() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestPlatformWheelBuild_resolveBaseImage(t *testing.T) {
	const pinned = "quay.io/pypa/musllinux_1_2_x86_64:2026.03.20-1@sha256:5b6fe3ed82ff48748c5c528fc82e42674a5363890ed949923af65b0c5b5ef76c"
	tests := []struct {
		name        string
		platformTag string
		baseImage   string
		want        string
		wantErr     bool
	}{
		{
			name:        "manylinux2014",
			platformTag: "manylinux2014_x86_64",
			want:        "quay.io/pypa/manylinux2014_x86_64",
		},
		{
			name:        "manylinux_2_28",
			platformTag: "manylinux_2_28_x86_64",
			want:        "quay.io/pypa/manylinux_2_28_x86_64",
		},
		{
			name:        "manylinux_2_34",
			platformTag: "manylinux_2_34_x86_64",
			want:        "quay.io/pypa/manylinux_2_34_x86_64",
		},
		{
			name:        "compressed tag set with legacy baseline",
			platformTag: "manylinux1_x86_64.manylinux_2_28_x86_64",
			want:        "quay.io/pypa/manylinux_2_28_x86_64",
		},
		{
			name:        "musllinux_1_1",
			platformTag: "musllinux_1_1_x86_64",
			want:        "quay.io/pypa/musllinux_1_1_x86_64",
		},
		{
			name:        "musllinux_1_2",
			platformTag: "musllinux_1_2_x86_64",
			want:        "quay.io/pypa/musllinux_1_2_x86_64",
		},
		{
			name:        "explicit base image is used verbatim",
			platformTag: "musllinux_1_2_x86_64",
			baseImage:   pinned,
			want:        pinned,
		},
		{
			name:        "explicit base image skips platform tag selection",
			platformTag: "invalid_tag",
			baseImage:   pinned,
			want:        pinned,
		},
		{
			name:        "empty platform tag with explicit base image returns error",
			platformTag: "",
			baseImage:   pinned,
			wantErr:     true,
		},
		{
			name:        "empty platform tag returns error",
			platformTag: "",
			wantErr:     true,
		},
		{
			name:        "invalid platform tag returns error",
			platformTag: "invalid_tag",
			wantErr:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &PlatformWheelBuild{PlatformTag: tt.platformTag, BaseImage: tt.baseImage}
			got, err := b.resolveBaseImage()
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveBaseImage() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("resolveBaseImage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNeedsVenvOnPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		reqs []string
		want string
	}{
		{"UVBuild", []string{"uv-build==0.10.0"}, "1"},
		{"MesonPython", []string{"meson-python>=0.15.0"}, "1"},
		{"ScikitBuildCore", []string{"scikit-build-core==0.10.7"}, "1"},
		{"Maturin", []string{"maturin==1.7.4"}, "1"},
		{"SetuptoolsOnly", []string{"setuptools>=61", "wheel"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsVenvOnPath(tc.reqs); got != tc.want {
				t.Errorf("needsVenvOnPath(%v) = %q, want %q", tc.reqs, got, tc.want)
			}
		})
	}
}

func TestPlatformWheelBuildVenvOnPath(t *testing.T) {
	loc := rebuild.Location{
		Dir:  "the_dir",
		Ref:  "the_ref",
		Repo: "the_repo",
	}
	s := &PlatformWheelBuild{
		Location:     loc,
		PythonTag:    "cp311",
		ABITag:       "cp311",
		Requirements: []string{"meson-python>=0.15.0", "ninja"},
		PlatformTag:  "manylinux_2_17_x86_64",
	}
	inst, err := s.GenerateFor(
		rebuild.Target{Ecosystem: rebuild.PyPI, Package: "the_package", Version: "the_version", Artifact: "the_artifact"},
		rebuild.BuildEnv{HasRepo: true, TimewarpHost: "orange"},
	)
	if err != nil {
		t.Fatalf("GenerateFor() failed unexpectedly: %v", err)
	}
	wantBuild := `PATH=/deps/bin/:$PATH /deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
$AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 --only-plat -w the_dir/dist/repaired/
rm -f the_dir/dist/*.whl
mv the_dir/dist/repaired/*.whl the_dir/dist/the_artifact
rm -rf the_dir/dist/repaired`
	if diff := cmp.Diff(wantBuild, inst.Build); diff != "" {
		t.Errorf("GenerateFor() Build diff (-want +got):\n%s", diff)
	}
}

func TestValidateBuildRoot(t *testing.T) {
	tests := []struct {
		name      string
		buildRoot string
		wantErr   bool
	}{
		{name: "Unset"},
		{name: "Cibuildwheel", buildRoot: "/project"},
		{name: "Nested", buildRoot: "/tmp/build/greenlet"},
		{name: "AzurePipelines", buildRoot: "/__w/1/s"},
		{name: "ShellSafePunctuation", buildRoot: "/home/user@host/c++,v2"},
		{name: "SharesCheckoutPrefix", buildRoot: "/srcdir"},
		{name: "CIWorkspace", buildRoot: "/opt/buildagent/work/abc"},
		{name: "DockerWorkdir", buildRoot: "/usr/src/app"},
		{name: "Relative", buildRoot: "project", wantErr: true},
		{name: "FilesystemRoot", buildRoot: "/", wantErr: true},
		{name: "TrailingSlash", buildRoot: "/project/", wantErr: true},
		{name: "ParentSegment", buildRoot: "/tmp/../project", wantErr: true},
		{name: "Whitespace", buildRoot: "/my project", wantErr: true},
		{name: "ShellMetacharacter", buildRoot: "/project;true", wantErr: true},
		{name: "Checkout", buildRoot: "/src", wantErr: true},
		{name: "InsideCheckout", buildRoot: "/src/project", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &PlatformWheelBuild{PlatformTag: "manylinux2014_x86_64", BuildRoot: tt.buildRoot}
			if _, err := b.ToWorkflow(); (err != nil) != tt.wantErr {
				t.Errorf("ToWorkflow() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateBuildEnvPath(t *testing.T) {
	tests := []struct {
		name         string
		buildEnvPath string
		wantErr      bool
	}{
		{name: "Unset"},
		{name: "BuildEnv", buildEnvPath: "/tmp/build-env-v9c0qwj_"},
		{name: "PipBuildEnvOverlay", buildEnvPath: "/tmp/pip-build-env-zeqt36e6/overlay"},
		{name: "PipBuildEnvNormal", buildEnvPath: "/tmp/pip-build-env-zeqt36e6/normal"},
		{name: "OutsideTmp", buildEnvPath: "/var/tmp/build-env-v9c0qwj_"},
		{name: "Relative", buildEnvPath: "tmp/build-env-v9c0qwj_", wantErr: true},
		{name: "TrailingSlash", buildEnvPath: "/tmp/build-env-v9c0qwj_/", wantErr: true},
		{name: "ShellMetacharacter", buildEnvPath: "/tmp/build-env-abc;true", wantErr: true},
		{name: "InsideCheckout", buildEnvPath: "/src/venv", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &PlatformWheelBuild{PlatformTag: "manylinux2014_x86_64", BuildEnvPath: tt.buildEnvPath}
			if _, err := b.ToWorkflow(); (err != nil) != tt.wantErr {
				t.Errorf("ToWorkflow() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
