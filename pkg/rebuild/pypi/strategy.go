// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"path"
	re "regexp"
	"slices"
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
// beside the interpreter, such as uv_build, meson, ninja/cmake, and maturin.
func needsVenvOnPath(reqs []string) string {
	if hasRequirement(reqs, "uv-build", "meson-python", "scikit-build-core", "maturin") {
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

// MaturinOptions configures a direct "maturin build" wheel build.
type MaturinOptions struct {
	// Features lists Cargo features passed via "--features" to "maturin build",
	// such as "abi3" when Cargo.toml gates "pyo3/abi3-*" behind a non-default feature.
	Features []string `json:"features,omitempty" yaml:"features,omitempty"`
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
	// When unset, the latest image of the family matching PlatformTag is used.
	BaseImage string `json:"base_image,omitempty" yaml:"base_image,omitempty"`
	// BuildRoot is the absolute path the build is run from, when set.
	BuildRoot string `json:"build_root,omitempty" yaml:"build_root,omitempty"`
	// RustVersion is the pinned rustup toolchain version (such as "1.97.1" or
	// "nightly-2026-05-01") to install when the wheel compiles Rust extensions.
	// Empty means no Rust toolchain is installed.
	RustVersion string `json:"rust_version,omitempty" yaml:"rust_version,omitempty"`
	// Maturin runs "maturin build --release --compatibility <policy>" directly
	// instead of PEP 517 plus "auditwheel repair" when non-nil.
	Maturin *MaturinOptions `json:"maturin,omitempty" yaml:"maturin,omitempty"`
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

// checkoutDir is where the build planners check out the repository.
const checkoutDir = "/src"

// buildRootPat matches absolute paths of characters that need no shell quoting.
var buildRootPat = re.MustCompile(`^(/[A-Za-z0-9._+,@-]+)+$`)

// validateBuildRoot rejects build roots that the build step cannot use.
func validateBuildRoot(root string) error {
	switch {
	case root == "":
		return nil
	case !buildRootPat.MatchString(root):
		return errors.Errorf("build root %q is not an absolute path of letters, digits and '._+,@-'", root)
	case path.Clean(root) != root:
		return errors.Errorf("build root %q is not a clean path", root)
	case root == checkoutDir || strings.HasPrefix(root, checkoutDir+"/"):
		return errors.Errorf("build root %q is in the checkout at %s", root, checkoutDir)
	}
	return nil
}

func (b *PlatformWheelBuild) ToWorkflow() (*rebuild.WorkflowStrategy, error) {
	baseImage, err := b.resolveBaseImage()
	if err != nil {
		return nil, errors.Wrap(err, "selecting base image")
	}
	if err := validateBuildRoot(b.BuildRoot); err != nil {
		return nil, errors.Wrap(err, "validating build root")
	}
	if b.Maturin != nil {
		if b.RustVersion == "" {
			return nil, errors.New("maturin build requires rust version")
		}
		for _, feat := range b.Maturin.Features {
			if !validMaturinFeaturePat.MatchString(feat) {
				return nil, errors.Errorf("invalid maturin feature %q", feat)
			}
		}
	}
	var registryTime string
	if !b.RegistryTime.IsZero() {
		registryTime = b.RegistryTime.Format(time.RFC3339)
	}
	distDir := "dist"
	if b.Location.Dir != "" {
		distDir = b.Location.Dir + "/dist"
	}
	var nonCanonicalName string
	// wheel tags sorts compressed platform tags lexicographically.
	if !slices.IsSorted(strings.Split(b.PlatformTag, ".")) {
		nonCanonicalName = "1"
	}
	depsSteps := []flow.Step{{
		Uses: "pypi/deps/platform-wheel",
		With: map[string]string{
			"registryTime": registryTime,
			"requirements": flow.MustToJSON(b.Requirements),
			"pythonTag":    b.PythonTag,
			"abiTag":       b.ABITag,
			"venv":         "/deps",
			"wheelCli":     "/deps/wheel-cli",
		},
	}}
	var rustHostTriple string
	if b.RustVersion != "" {
		if !isValidRustVersion(b.RustVersion) {
			return nil, errors.Errorf("invalid rust version %q", b.RustVersion)
		}
		host, err := rustHostForPlatform(b.PlatformTag)
		if err != nil {
			return nil, errors.Wrap(err, "selecting rust toolchain host")
		}
		rustHostTriple = host.triple
		depsSteps = append(depsSteps, flow.Step{
			Uses: "pypi/install-rust",
			With: map[string]string{
				"version":       b.RustVersion,
				"host":          host.triple,
				"rustupVersion": rustupVersion,
				"rustupSHA256":  host.rustupSHA256,
			},
		})
	}
	var buildStep flow.Step
	if b.Maturin != nil {
		compatibilityPolicy, err := platform.HighestPolicy(b.PlatformTag)
		if err != nil {
			return nil, errors.Wrap(err, "selecting maturin compatibility policy")
		}
		var retagABI string
		if strings.Contains(b.ABITag, ".") {
			retagABI = b.ABITag
		}
		buildStep = flow.Step{
			Uses: "pypi/build/maturin-wheel",
			With: map[string]string{
				"dir":                 b.Location.Dir,
				"distDir":             distDir,
				"buildRoot":           b.BuildRoot,
				"locator":             "/deps/bin/",
				"pythonBin":           "/deps/bin/python3",
				"wheelCli":            "/deps/wheel-cli",
				"compatibilityPolicy": compatibilityPolicy,
				"targetPlatformTag":   b.PlatformTag,
				"retagABI":            retagABI,
				"nonCanonicalName":    nonCanonicalName,
				"rustVersion":         b.RustVersion,
				"rustHost":            rustHostTriple,
				"maturinFeatures":     strings.Join(b.Maturin.Features, ","),
			},
		}
	} else {
		buildStep = flow.Step{
			Uses: "pypi/build/platform-wheel",
			With: map[string]string{
				"dir":        b.Location.Dir,
				"distDir":    distDir,
				"buildRoot":  b.BuildRoot,
				"locator":    "/deps/bin/",
				"wheelCli":   "/deps/wheel-cli",
				"venvOnPath": needsVenvOnPath(b.Requirements),
				// auditwheel repair --plat requires a single policy tag rather than a compressed
				// tag set, and the highest tag matches the build container policy.
				"highestPlatformTag": platform.HighestLibcTagString(b.PlatformTag),
				"targetPlatformTag":  b.PlatformTag,
				"nonCanonicalName":   nonCanonicalName,
				"rustVersion":        b.RustVersion,
				"rustHost":           rustHostTriple,
			},
		}
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
		Deps:      depsSteps,
		Build:     []flow.Step{buildStep},
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
	{
		Name: "pypi/install-rust",
		Steps: []flow.Step{{
			// NOTE: CARGO_HOME and RUSTUP_HOME must match pypi/build/platform-wheel and
			// pypi/build/maturin-wheel. Installing under /root/.cargo and /root/.rustup
			// makes crate registry source paths embedded in .rodata match upstream PyPA
			// container builds.
			Runs: textwrap.Dedent(`
				wget -O rustup-init https://static.rust-lang.org/rustup/archive/{{.With.rustupVersion}}/{{.With.host}}/rustup-init
				echo '{{.With.rustupSHA256}}  rustup-init' | sha256sum -c -
				chmod +x rustup-init
				CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup ./rustup-init -y --profile minimal --default-toolchain {{.With.version}} --no-modify-path
				rm -f rustup-init`)[1:],
			Needs: []string{"wget"},
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
				// Isolate a modern wheel CLI for re-tagging, before timewarp. The
				// `wheel tags` subcommand arrived in wheel 0.38.0, while the wheel
				// pinned by the requirements below must stay what bdist_wheel ran
				// with, as it stamps its version into the WHEEL file.
				Runs: "{{.With.venv}}/bin/pip install --target {{.With.wheelCli}} wheel",
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
				{{- if .With.rustVersion -}}
				{{- /* NOTE: CARGO_HOME, RUSTUP_HOME, and PATH must match pypi/install-rust. */ -}}
				export CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup RUSTUP_TOOLCHAIN={{.With.rustVersion}} PATH=/root/.rustup/toolchains/{{.With.rustVersion}}-{{.With.rustHost}}/bin:/root/.cargo/bin:$PATH
				{{end -}}
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
				$AUDITWHEEL repair {{.With.distDir}}/*.whl --plat {{.With.highestPlatformTag}} -w {{.With.distDir}}/repaired/
				rm -f {{.With.distDir}}/*.whl
				mv {{.With.distDir}}/repaired/*.whl {{.With.distDir}}/
				rm -rf {{.With.distDir}}/repaired
				{{end -}}
				{{if .With.targetPlatformTag -}}
				PYTHONPATH={{.With.wheelCli}} {{.With.locator}}python3 -m wheel tags --remove --platform-tag {{.With.targetPlatformTag}} {{.With.distDir}}/*.whl
				{{- end -}}
				{{- if .With.nonCanonicalName}}
				mv {{.With.distDir}}/*.whl {{.With.distDir}}/{{.Target.Artifact}}
				{{- end -}}`)[1:],
		}},
	},
	{
		Name: "pypi/build/maturin-wheel",
		Steps: []flow.Step{{
			// NOTE: For single application binary interface (ABI) tags, "maturin build
			// --compatibility <policy>" emits the target wheel filename and archive layout
			// directly. Retagging runs only when retagABI is set for compound dot-separated
			// ABI tags such as "abi3.abi3t".
			Runs: textwrap.Dedent(`
				{{- if .With.rustVersion -}}
				{{- /* NOTE: CARGO_HOME, RUSTUP_HOME, and PATH must match pypi/install-rust. */ -}}
				export CARGO_HOME=/root/.cargo RUSTUP_HOME=/root/.rustup RUSTUP_TOOLCHAIN={{.With.rustVersion}} PATH=/root/.rustup/toolchains/{{.With.rustVersion}}-{{.With.rustHost}}/bin:/root/.cargo/bin:$PATH
				{{end -}}
				{{- if .With.buildRoot -}}
				{{- /* NOTE: No -p on the root so the copy can't merge into an existing directory. */ -}}
				mkdir -p $(dirname {{.With.buildRoot}})
				mkdir {{.With.buildRoot}}
				cp -a . {{.With.buildRoot}}
				cd {{.With.buildRoot}}
				{{end -}}
				{{- if and (ne .With.dir ".") (ne .With.dir "") -}}
				cd {{.With.dir}}
				{{end -}}
				{{.With.locator}}maturin build --release --out {{if .With.buildRoot}}/src/{{.With.distDir}}{{else}}dist{{end}} -i {{.With.pythonBin}}{{if .With.compatibilityPolicy}} --compatibility {{.With.compatibilityPolicy}}{{end}}{{if .With.maturinFeatures}} --features {{.With.maturinFeatures}}{{end}}
				{{- if .With.retagABI}}
				PYTHONPATH={{.With.wheelCli}} {{.With.locator}}python3 -m wheel tags --remove --abi-tag {{.With.retagABI}} --platform-tag {{.With.targetPlatformTag}} {{if .With.buildRoot}}/src/{{.With.distDir}}{{else}}dist{{end}}/*.whl
				{{- if .With.nonCanonicalName}}
				mv {{if .With.buildRoot}}/src/{{.With.distDir}}{{else}}dist{{end}}/*.whl {{if .With.buildRoot}}/src/{{.With.distDir}}{{else}}dist{{end}}/{{.Target.Artifact}}
				{{- end -}}
				{{- end -}}`)[1:],
		}},
	},
}
