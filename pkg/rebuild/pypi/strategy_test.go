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
				Deps: `INTERPRETER=""
if [ -n "cp310" ]; then
  if [ -n "cp310" ] && [ -d "/opt/python/cp310-cp310" ]; then
    INTERPRETER="/opt/python/cp310-cp310/bin/python"
  else
    for dir in /opt/python/cp310*; do
      if [ -d "$dir" ]; then
        INTERPRETER="$dir/bin/python"
        break
      fi
    done
  fi
  if [ -z "$INTERPRETER" ]; then
    echo "Error: Requested Python tag 'cp310' not found in /opt/python" >&2
    exit 1
  fi
elif [ -d "/opt/python/cp310-cp310" ]; then
  INTERPRETER="/opt/python/cp310-cp310/bin/python"
else
  for dir in /opt/python/*; do
    if [ -d "$dir" ]; then
      INTERPRETER="$dir/bin/python"
    fi
  done
fi
$INTERPRETER -m venv /deps
/deps/bin/pip install build wheel auditwheel
/deps/bin/pip install 'req_1'
/deps/bin/pip install 'req_2'`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
if $AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 -w the_dir/dist/repaired/; then
  rm -f the_dir/dist/*.whl
  mv the_dir/dist/repaired/*.whl the_dir/dist/
fi
rm -rf the_dir/dist/repaired
/deps/bin/python3 -m wheel tags --remove --platform-tag manylinux_2_17_x86_64 the_dir/dist/*.whl`,
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
				Deps: `INTERPRETER=""
if [ -n "cp310" ]; then
  if [ -n "cp310" ] && [ -d "/opt/python/cp310-cp310" ]; then
    INTERPRETER="/opt/python/cp310-cp310/bin/python"
  else
    for dir in /opt/python/cp310*; do
      if [ -d "$dir" ]; then
        INTERPRETER="$dir/bin/python"
        break
      fi
    done
  fi
  if [ -z "$INTERPRETER" ]; then
    echo "Error: Requested Python tag 'cp310' not found in /opt/python" >&2
    exit 1
  fi
elif [ -d "/opt/python/cp310-cp310" ]; then
  INTERPRETER="/opt/python/cp310-cp310/bin/python"
else
  for dir in /opt/python/*; do
    if [ -d "$dir" ]; then
      INTERPRETER="$dir/bin/python"
    fi
  done
fi
$INTERPRETER -m venv /deps
/deps/bin/pip install build wheel auditwheel
/deps/bin/pip install 'req_1<='\''1.2.3'\'''`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
if $AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 -w the_dir/dist/repaired/; then
  rm -f the_dir/dist/*.whl
  mv the_dir/dist/repaired/*.whl the_dir/dist/
fi
rm -rf the_dir/dist/repaired
/deps/bin/python3 -m wheel tags --remove --platform-tag manylinux_2_17_x86_64 the_dir/dist/*.whl`,
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
				Deps: `INTERPRETER=""
if [ -n "cp310" ]; then
  if [ -n "cp310" ] && [ -d "/opt/python/cp310-cp310" ]; then
    INTERPRETER="/opt/python/cp310-cp310/bin/python"
  else
    for dir in /opt/python/cp310*; do
      if [ -d "$dir" ]; then
        INTERPRETER="$dir/bin/python"
        break
      fi
    done
  fi
  if [ -z "$INTERPRETER" ]; then
    echo "Error: Requested Python tag 'cp310' not found in /opt/python" >&2
    exit 1
  fi
elif [ -d "/opt/python/cp310-cp310" ]; then
  INTERPRETER="/opt/python/cp310-cp310/bin/python"
else
  for dir in /opt/python/*; do
    if [ -d "$dir" ]; then
      INTERPRETER="$dir/bin/python"
    fi
  done
fi
$INTERPRETER -m venv /deps
/deps/bin/pip install build wheel auditwheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
if $AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 -w the_dir/dist/repaired/; then
  rm -f the_dir/dist/*.whl
  mv the_dir/dist/repaired/*.whl the_dir/dist/
fi
rm -rf the_dir/dist/repaired
/deps/bin/python3 -m wheel tags --remove --platform-tag manylinux_2_17_x86_64 the_dir/dist/*.whl`,
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
				Deps: `INTERPRETER=""
if [ -n "" ]; then
  if [ -n "" ] && [ -d "/opt/python/-" ]; then
    INTERPRETER="/opt/python/-/bin/python"
  else
    for dir in /opt/python/*; do
      if [ -d "$dir" ]; then
        INTERPRETER="$dir/bin/python"
        break
      fi
    done
  fi
  if [ -z "$INTERPRETER" ]; then
    echo "Error: Requested Python tag '' not found in /opt/python" >&2
    exit 1
  fi
elif [ -d "/opt/python/cp310-cp310" ]; then
  INTERPRETER="/opt/python/cp310-cp310/bin/python"
else
  for dir in /opt/python/*; do
    if [ -d "$dir" ]; then
      INTERPRETER="$dir/bin/python"
    fi
  done
fi
$INTERPRETER -m venv /deps
/deps/bin/pip install build wheel auditwheel
export PIP_INDEX_URL=http://pypi:2006-01-02T03:04:05Z@orange/simple`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
if $AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_17_x86_64 -w the_dir/dist/repaired/; then
  rm -f the_dir/dist/*.whl
  mv the_dir/dist/repaired/*.whl the_dir/dist/
fi
rm -rf the_dir/dist/repaired
/deps/bin/python3 -m wheel tags --remove --platform-tag manylinux_2_17_x86_64 the_dir/dist/*.whl`,
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
				Deps: `INTERPRETER=""
if [ -n "" ]; then
  if [ -n "" ] && [ -d "/opt/python/-" ]; then
    INTERPRETER="/opt/python/-/bin/python"
  else
    for dir in /opt/python/*; do
      if [ -d "$dir" ]; then
        INTERPRETER="$dir/bin/python"
        break
      fi
    done
  fi
  if [ -z "$INTERPRETER" ]; then
    echo "Error: Requested Python tag '' not found in /opt/python" >&2
    exit 1
  fi
elif [ -d "/opt/python/cp310-cp310" ]; then
  INTERPRETER="/opt/python/cp310-cp310/bin/python"
else
  for dir in /opt/python/*; do
    if [ -d "$dir" ]; then
      INTERPRETER="$dir/bin/python"
    fi
  done
fi
$INTERPRETER -m venv /deps
/deps/bin/pip install build wheel auditwheel`,
				Build: `/deps/bin/python3 -m build --wheel -n
mkdir -p dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
if $AUDITWHEEL repair dist/*.whl --plat manylinux_2_17_x86_64 -w dist/repaired/; then
  rm -f dist/*.whl
  mv dist/repaired/*.whl dist/
fi
rm -rf dist/repaired
/deps/bin/python3 -m wheel tags --remove --platform-tag manylinux_2_17_x86_64 dist/*.whl`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
					SystemDeps: []string{"git"},
				},
				OutputPath: "dist/the_artifact",
			},
		},
		{
			"CompressedTagSet",
			&PlatformWheelBuild{
				Location:    defaultLocation,
				PythonTag:   "cp310",
				ABITag:      "cp310",
				PlatformTag: "manylinux1_x86_64.manylinux_2_28_x86_64.manylinux_2_5_x86_64",
			},
			rebuild.Instructions{
				Location: defaultLocation,
				Source:   "git checkout --force 'the_ref'",
				Deps: `INTERPRETER=""
if [ -n "cp310" ]; then
  if [ -n "cp310" ] && [ -d "/opt/python/cp310-cp310" ]; then
    INTERPRETER="/opt/python/cp310-cp310/bin/python"
  else
    for dir in /opt/python/cp310*; do
      if [ -d "$dir" ]; then
        INTERPRETER="$dir/bin/python"
        break
      fi
    done
  fi
  if [ -z "$INTERPRETER" ]; then
    echo "Error: Requested Python tag 'cp310' not found in /opt/python" >&2
    exit 1
  fi
elif [ -d "/opt/python/cp310-cp310" ]; then
  INTERPRETER="/opt/python/cp310-cp310/bin/python"
else
  for dir in /opt/python/*; do
    if [ -d "$dir" ]; then
      INTERPRETER="$dir/bin/python"
    fi
  done
fi
$INTERPRETER -m venv /deps
/deps/bin/pip install build wheel auditwheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
if $AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux1_x86_64 -w the_dir/dist/repaired/; then
  rm -f the_dir/dist/*.whl
  mv the_dir/dist/repaired/*.whl the_dir/dist/
fi
rm -rf the_dir/dist/repaired
/deps/bin/python3 -m wheel tags --remove --platform-tag manylinux1_x86_64.manylinux_2_28_x86_64.manylinux_2_5_x86_64 the_dir/dist/*.whl`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux2014_x86_64",
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
				Deps: `INTERPRETER=""
if [ -n "cp38" ]; then
  if [ -n "cp38" ] && [ -d "/opt/python/cp38-cp38" ]; then
    INTERPRETER="/opt/python/cp38-cp38/bin/python"
  else
    for dir in /opt/python/cp38*; do
      if [ -d "$dir" ]; then
        INTERPRETER="$dir/bin/python"
        break
      fi
    done
  fi
  if [ -z "$INTERPRETER" ]; then
    echo "Error: Requested Python tag 'cp38' not found in /opt/python" >&2
    exit 1
  fi
elif [ -d "/opt/python/cp310-cp310" ]; then
  INTERPRETER="/opt/python/cp310-cp310/bin/python"
else
  for dir in /opt/python/*; do
    if [ -d "$dir" ]; then
      INTERPRETER="$dir/bin/python"
    fi
  done
fi
$INTERPRETER -m venv /deps
/deps/bin/pip install build wheel auditwheel`,
				Build: `/deps/bin/python3 -m build --wheel -n the_dir
mkdir -p the_dir/dist/repaired
AUDITWHEEL="/deps/bin/auditwheel"
if [ ! -x "$AUDITWHEEL" ]; then
  AUDITWHEEL="auditwheel"
fi
if $AUDITWHEEL repair the_dir/dist/*.whl --plat manylinux_2_28_x86_64 -w the_dir/dist/repaired/; then
  rm -f the_dir/dist/*.whl
  mv the_dir/dist/repaired/*.whl the_dir/dist/
fi
rm -rf the_dir/dist/repaired
/deps/bin/python3 -m wheel tags --remove --platform-tag manylinux_2_28_x86_64 the_dir/dist/*.whl`,
				Requires: rebuild.RequiredEnv{
					BaseImage:  "quay.io/pypa/manylinux_2_28_x86_64",
					SystemDeps: []string{"git"},
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

func TestPlatformWheelBuild_BaseImage(t *testing.T) {
	tests := []struct {
		name        string
		platformTag string
		want        string
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
			want:        "quay.io/pypa/manylinux2014_x86_64",
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
			name:        "empty platform tag",
			platformTag: "",
			want:        "quay.io/pypa/manylinux2014_x86_64",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &PlatformWheelBuild{PlatformTag: tt.platformTag}
			if got := b.BaseImage(); got != tt.want {
				t.Errorf("BaseImage() = %q, want %q", got, tt.want)
			}
		})
	}
}
