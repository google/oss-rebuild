// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"path"
	re "regexp"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// PureWheelBuild aggregates the options controlling a wheel build.
type PureWheelBuild struct {
	rebuild.Location
	PythonVersion string    `json:"python_version" yaml:"python_version"`
	PythonTag     string    `json:"python_tag,omitempty" yaml:"python_tag,omitempty"`
	Requirements  []string  `json:"requirements" yaml:"requirements"`
	RegistryTime  time.Time `json:"registry_time" yaml:"registry_time,omitempty"`
}

var _ rebuild.Strategy = &PureWheelBuild{}

func (b *PureWheelBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	var registryTime string
	if !b.RegistryTime.IsZero() {
		registryTime = b.RegistryTime.Format(time.RFC3339)
	}
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "pypi/deps/basic",
			With: map[string]string{
				"registryTime":  registryTime,
				"requirements":  flow.MustToJSON(b.Requirements),
				"pythonVersion": b.PythonVersion,
				"venv":          "/deps",
			},
		}},
		Build: []flow.Step{{
			Uses: "pypi/build/wheel",
			With: map[string]string{
				"dir":        b.Location.Dir,
				"locator":    "/deps/bin/",
				"venvOnPath": needsVenvOnPath(b.Requirements),
				"pythonTag":  b.PythonTag,
			},
		}},
		OutputDir: func() string {
			if b.Location.Dir != "" {
				return b.Location.Dir + "/dist"
			}
			return "dist"
		}(),
	}
}

// GenerateFor generates the instructions for a PureWheelBuild.
func (b *PureWheelBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

// needsVenvOnPath flags backends that find their binary on PATH rather than
// beside the interpreter, such as uv_build, meson, and ninja/cmake.
func needsVenvOnPath(reqs []string) string {
	if hasRequirement(reqs, "uv-build", "meson-python", "scikit-build-core") {
		return "1"
	}
	return ""
}

// SdistBuild includes elements for building an sdist.
type SdistBuild struct {
	rebuild.Location
	PythonVersion string    `json:"python_version" yaml:"python_version"`
	Requirements  []string  `json:"requirements" yaml:"requirements"`
	RegistryTime  time.Time `json:"registry_time" yaml:"registry_time,omitempty"`
}

var _ rebuild.Strategy = &SdistBuild{}

func (b *SdistBuild) ToWorkflow() *rebuild.WorkflowStrategy {
	var registryTime string
	if !b.RegistryTime.IsZero() {
		registryTime = b.RegistryTime.Format(time.RFC3339)
	}
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "pypi/deps/basic",
			With: map[string]string{
				"registryTime":  registryTime,
				"requirements":  flow.MustToJSON(b.Requirements),
				"pythonVersion": b.PythonVersion,
				"venv":          "/deps",
			},
		}},
		Build: []flow.Step{{
			Uses: "pypi/build/sdist",
			With: map[string]string{
				"dir":        b.Location.Dir,
				"locator":    "/deps/bin/",
				"venvOnPath": needsVenvOnPath(b.Requirements),
			},
		}},
		OutputDir: func() string {
			if b.Location.Dir != "" {
				return b.Location.Dir + "/dist"
			}
			return "dist"
		}(),
	}
}

// GenerateFor generates the instructions for a SourceDistBuild.
func (b *SdistBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

// PlatformWheelBuild aggregates the options controlling a platform-specific wheel build.
type PlatformWheelBuild struct {
	rebuild.Location
	PythonTag    string    `json:"python_tag,omitempty" yaml:"python_tag,omitempty"`
	ABITag       string    `json:"abi_tag,omitempty" yaml:"abi_tag,omitempty"`
	Requirements []string  `json:"requirements" yaml:"requirements"`
	PlatformTag  string    `json:"platform_tag,omitempty" yaml:"platform_tag,omitempty"`
	SystemDeps   []string  `json:"system_deps,omitempty" yaml:"system_deps,omitempty"`
	RegistryTime time.Time `json:"registry_time" yaml:"registry_time,omitempty"`
	// BaseImage is the build image, used verbatim when set. It should be pinned by
	// digest, as in "quay.io/pypa/musllinux_1_2_x86_64:2026.03.20-1@sha256:<digest>",
	// because the image determines the compiler, CPython builds and auditwheel.
	// Legacy-only tags such as "manylinux2014_x86_64" need auditwheel < 4.0.0.
	// When unset, the latest image of the family matching PlatformTag is used.
	BaseImage string `json:"base_image,omitempty" yaml:"base_image,omitempty"`
	// BuildRoot is the absolute path the build is run from, when set.
	BuildRoot string `json:"build_root,omitempty" yaml:"build_root,omitempty"`
	// BuildEnvPath is the isolated virtual environment path build dependencies are installed into, when set.
	BuildEnvPath string `json:"build_env_path,omitempty" yaml:"build_env_path,omitempty"`
}

var _ rebuild.Strategy = &PlatformWheelBuild{}

func (b *PlatformWheelBuild) resolveBaseImage() (string, error) {
	if b.PlatformTag == "" {
		return "", errors.New("platform tag is required")
	}
	if b.BaseImage != "" {
		return b.BaseImage, nil
	}
	return platform.SelectBaseImage(b.PlatformTag)
}

const (
	// checkoutDir is where the build planners check out the repository.
	checkoutDir = "/src"
	// venvDir is where the dependency steps create the virtual environment by default.
	venvDir = "/deps"
)

// buildPathPat matches absolute paths of characters that need no shell quoting.
var buildPathPat = re.MustCompile(`^(/[A-Za-z0-9._+,@-]+)+$`)

// validateBuildPath rejects paths that the build steps cannot use.
func validateBuildPath(p string) error {
	switch {
	case p == "":
		return nil
	case !buildPathPat.MatchString(p):
		return errors.Errorf("path %q is not an absolute path of letters, digits and '._+,@-'", p)
	case path.Clean(p) != p:
		return errors.Errorf("path %q is not a clean path", p)
	case p == checkoutDir || strings.HasPrefix(p, checkoutDir+"/"):
		return errors.Errorf("path %q is in the checkout at %s", p, checkoutDir)
	}
	return nil
}

func (b *PlatformWheelBuild) ToWorkflow() (*rebuild.WorkflowStrategy, error) {
	baseImage, err := b.resolveBaseImage()
	if err != nil {
		return nil, errors.Wrap(err, "selecting base image")
	}
	if err := validateBuildPath(b.BuildRoot); err != nil {
		return nil, errors.Wrap(err, "validating build root")
	}
	if err := validateBuildPath(b.BuildEnvPath); err != nil {
		return nil, errors.Wrap(err, "validating build env path")
	}
	venv := venvDir
	if b.BuildEnvPath != "" {
		venv = b.BuildEnvPath
	}
	var registryTime string
	if !b.RegistryTime.IsZero() {
		registryTime = b.RegistryTime.Format(time.RFC3339)
	}
	distDir := "dist"
	if b.Location.Dir != "" {
		distDir = b.Location.Dir + "/dist"
	}
	var onlyPlat string
	// A single PEP 600 tag means upstream did not accept a higher policy. --only-plat
	// stops auditwheel from prepending one.
	// NOTE: requires auditwheel >= 4.0.0. It does not drop the policy's legacy
	// alias (manylinux_2_17 -> manylinux2014), which is still emitted.
	if !strings.Contains(b.PlatformTag, ".") && strings.HasPrefix(b.PlatformTag, "manylinux_") {
		onlyPlat = "true"
	}
	return &rebuild.WorkflowStrategy{
		Location: b.Location,
		Requires: rebuild.RequiredEnv{
			BaseImage:  baseImage,
			SystemDeps: b.SystemDeps,
		},
		Source: []flow.Step{{
			Uses: "git-checkout",
		}},
		Deps: []flow.Step{{
			Uses: "pypi/deps/platform-wheel",
			With: map[string]string{
				"registryTime": registryTime,
				"requirements": flow.MustToJSON(b.Requirements),
				"pythonTag":    b.PythonTag,
				"abiTag":       b.ABITag,
				"venv":         venv,
			},
		}},
		Build: []flow.Step{{
			Uses: "pypi/build/platform-wheel",
			With: map[string]string{
				"dir":        b.Location.Dir,
				"distDir":    distDir,
				"buildRoot":  b.BuildRoot,
				"locator":    venv + "/bin/",
				"venvOnPath": needsVenvOnPath(b.Requirements),
				// auditwheel repair --plat requires a single policy tag rather than a compressed
				// tag set, and the highest tag matches the build container policy.
				"highestPlatformTag": platform.HighestLibcTagString(b.PlatformTag),
				"onlyPlat":           onlyPlat,
			},
		}},
		OutputDir: distDir,
	}, nil
}

// GenerateFor generates the instructions for a PlatformWheelBuild.
func (b *PlatformWheelBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	wf, err := b.ToWorkflow()
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return wf.GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

// Base tools for individual operations
var toolkit = []*flow.Tool{
	{
		Name: "pypi/setup-venv",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if .With.pythonVersion -}}
				{{.With.locator}}uvx uv venv {{.With.path}} --seed --python {{.With.pythonVersion}}
				{{- else -}}
				{{.With.locator}}python3 -m venv {{.With.path}}
				{{- end -}}`)[1:],
			Needs: []string{"python3", "uv"},
		}},
	},
	{
		Name: "pypi/setup-venv/manylinux",
		Steps: []flow.Step{{
			// TODO: Support Python 2.7 (requires virtualenv instead of standard library venv).
			Runs: textwrap.Dedent(`
				for py in
				{{- if and .With.pythonTag .With.abiTag}} /opt/python/{{.With.pythonTag}}-{{.With.abiTag}}/bin/python{{end -}}
				{{- if .With.pythonTag}} /opt/python/{{.With.pythonTag}}-*/bin/python{{end -}}
				{{- if or (not .With.pythonTag) (eq .With.abiTag "abi3")}} /opt/python/cp3*/bin/python{{end -}}
				; do [ -x "$py" ] && break; done
				"$py" -m venv {{.With.path}}`)[1:],
		}},
	},
	{
		Name: "pypi/setup-registry",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if ne .With.registryTime "" -}}
				export PIP_INDEX_URL={{.BuildEnv.TimewarpURLFromString "pypi" .With.registryTime}}/simple
				{{- end -}}`)[1:],
			Needs: []string{},
		}},
	},
	{
		Name: "pypi/install-deps",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{range $i, $req := .With.requirements | fromJSON}}{{if $i}}
				{{end}}{{$.With.locator}}pip install '{{regexReplace $req "'" "'\\''"}}'{{end}}`)[1:],
		}},
	},

	// Composite tools for common workflow steps
	{
		Name: "pypi/deps/basic",
		Steps: []flow.Step{
			{
				Uses: "pypi/setup-venv",
				With: map[string]string{
					"locator":       "/usr/bin/",
					"path":          "{{.With.venv}}",
					"pythonVersion": "{{.With.pythonVersion}}",
				},
			},
			{
				// Fetch the PEP 517 frontend from the real index, before timewarp.
				// The frontend doesn't impact the output and contemporary
				// versions lacked CLI flags and features we use.
				Runs: "{{.With.venv}}/bin/pip install build",
			},
			{
				Uses: "pypi/setup-registry",
				With: map[string]string{
					"registryTime": "{{.With.registryTime}}",
				},
			},
			{
				Uses: "pypi/install-deps",
				With: map[string]string{
					"requirements": "{{.With.requirements}}",
					"locator":      "{{.With.venv}}/bin/",
				},
			},
		},
	},
	{
		Name: "pypi/deps/platform-wheel",
		Steps: []flow.Step{
			{
				Uses: "pypi/setup-venv/manylinux",
				With: map[string]string{
					"path":      "{{.With.venv}}",
					"pythonTag": "{{.With.pythonTag}}",
					"abiTag":    "{{.With.abiTag}}",
				},
			},
			{
				// Fetch the PEP 517 frontend and wheel from the real index, before timewarp.
				// auditwheel is deliberately absent. The build uses the one bundled with the
				// PyPA image, which matches the image's patchelf and is what upstream used
				// when building in the same image.
				Runs: "{{.With.venv}}/bin/pip install build wheel",
			},
			{
				Uses: "pypi/setup-registry",
				With: map[string]string{
					"registryTime": "{{.With.registryTime}}",
				},
			},
			{
				Uses: "pypi/install-deps",
				With: map[string]string{
					"requirements": "{{.With.requirements}}",
					"locator":      "{{.With.venv}}/bin/",
				},
			},
		},
	},
	{
		Name: "pypi/build/wheel",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{- if .With.pythonTag -}}
				printf '[bdist_wheel]\npython-tag = {{.With.pythonTag}}\n' >~/.pydistutils.cfg
				{{end -}}
				{{if .With.venvOnPath}}PATH={{.With.locator}}:$PATH {{end}}{{.With.locator}}python3 -m build --wheel -n{{if and (ne .With.dir ".") (ne .With.dir "")}} {{.With.dir}}{{end}}`)[1:],
		}},
	},
	{
		Name: "pypi/build/sdist",
		Steps: []flow.Step{
			{
				Runs: textwrap.Dedent(`
				{{if .With.venvOnPath}}PATH={{.With.locator}}:$PATH {{end}}{{.With.locator}}python3 -m build --sdist -n{{if and (ne .With.dir ".") (ne .With.dir "")}} {{.With.dir}}{{end}}`)[1:],
			}},
	},
	{
		Name: "pypi/build/platform-wheel",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{if .With.buildRoot -}}
				{{- /* NOTE: No -p on the root so the copy can't merge into an existing directory. */ -}}
				mkdir -p $(dirname {{.With.buildRoot}})
				mkdir {{.With.buildRoot}}
				cp -a . {{.With.buildRoot}}
				{{if .With.venvOnPath}}PATH={{.With.locator}}:$PATH {{end}}{{.With.locator}}python3 -m build --wheel -n --outdir {{.With.distDir}} {{.With.buildRoot}}{{if and (ne .With.dir ".") (ne .With.dir "")}}/{{.With.dir}}{{end}}
				{{else -}}
				{{if .With.venvOnPath}}PATH={{.With.locator}}:$PATH {{end}}{{.With.locator}}python3 -m build --wheel -n{{if and (ne .With.dir ".") (ne .With.dir "")}} {{.With.dir}}{{end}}
				{{end -}}
				{{if .With.highestPlatformTag -}}
				mkdir -p {{.With.distDir}}/repaired
				AUDITWHEEL="{{.With.locator}}auditwheel"
				if [ ! -x "$AUDITWHEEL" ]; then
				  AUDITWHEEL="auditwheel"
				fi
				$AUDITWHEEL repair {{.With.distDir}}/*.whl --plat {{.With.highestPlatformTag}}{{if .With.onlyPlat}} --only-plat{{end}} -w {{.With.distDir}}/repaired/
				rm -f {{.With.distDir}}/*.whl
				mv {{.With.distDir}}/repaired/*.whl {{.With.distDir}}/{{.Target.Artifact}}
				rm -rf {{.With.distDir}}/repaired
				{{- end -}}`)[1:],
		}},
	},
}
