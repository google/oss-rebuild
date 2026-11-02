// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/msuozzo/go-git2"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

// ProjectMetadata represents the [project] or [tool.poetry] table in pyproject.toml.
type ProjectMetadata struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

// CibuildwheelHooks represents build hooks in [tool.cibuildwheel], platform subtables, or override entries.
type CibuildwheelHooks struct {
	BeforeAll   any `toml:"before-all"`
	BeforeBuild any `toml:"before-build"`
}

// CibuildwheelOverride represents an entry in [[tool.cibuildwheel.overrides]].
type CibuildwheelOverride struct {
	Select any `toml:"select"`
	CibuildwheelHooks
}

// CibuildwheelConfig represents the [tool.cibuildwheel] table in pyproject.toml.
type CibuildwheelConfig struct {
	CibuildwheelHooks
	Linux     CibuildwheelHooks      `toml:"linux"`
	Overrides []CibuildwheelOverride `toml:"overrides"`
}

// ScikitBuildMetadata represents an entry in [tool.scikit-build.metadata.<field>].
type ScikitBuildMetadata struct {
	Provider string `toml:"provider"`
}

// ScikitBuildConfig represents the [tool.scikit-build] table in pyproject.toml.
type ScikitBuildConfig struct {
	Metadata map[string]ScikitBuildMetadata `toml:"metadata"`
}

// scikitBuildMetadataPlugins maps scikit-build-core metadata providers to the
// plugin package that get_requires_for_build_wheel requests for them.
// Built-in providers such as scikit_build_core.metadata.regex need no plugin.
// https://scikit-build-core.readthedocs.io/en/latest/configuration/dynamic.html
var scikitBuildMetadataPlugins = map[string]string{
	"scikit_build_core.metadata.setuptools_scm":    "setuptools-scm",
	"scikit_build_core.metadata.fancy_pypi_readme": "hatch-fancy-pypi-readme",
}

// ToolConfig represents the [tool] table in pyproject.toml.
type ToolConfig struct {
	Poetry       ProjectMetadata    `toml:"poetry"`
	Cibuildwheel CibuildwheelConfig `toml:"cibuildwheel"`
	ScikitBuild  ScikitBuildConfig  `toml:"scikit-build"`
}

// PyProject represents the structure of a pyproject.toml file.
type PyProject struct {
	Project     ProjectMetadata `toml:"project"`
	BuildSystem struct {
		Requires []string `toml:"requires"`
		Backend  string   `toml:"build-backend"`
	} `toml:"build-system"`
	Tool ToolConfig `toml:"tool"`
}

// ReadPyProject reads and unmarshals a pyproject.toml git object file.
func ReadPyProject(f *git2.File) (PyProject, error) {
	contents, err := f.Contents()
	if err != nil {
		return PyProject{}, errors.Wrap(err, "reading pyproject.toml")
	}
	var pyProject PyProject
	if err := toml.Unmarshal([]byte(contents), &pyProject); err != nil {
		return PyProject{}, errors.Wrap(err, "decoding pyproject.toml")
	}
	return pyProject, nil
}

func verifyPyProjectFile(ctx context.Context, f *git2.File, name, version string) (fileVerification, error) {
	var verificationResult fileVerification
	verificationResult.foundF = f
	pyProject, err := ReadPyProject(f)
	if err != nil {
		return verificationResult, err
	}
	foundName := ""
	foundVersion := ""
	if pyProject.Project.Name != "" {
		foundName = pyProject.Project.Name
		foundVersion = pyProject.Project.Version
	} else if pyProject.Tool.Poetry.Name != "" {
		foundName = pyProject.Tool.Poetry.Name
		foundVersion = pyProject.Tool.Poetry.Version
	}

	if filepath.Dir(f.Name) == "." {
		verificationResult.main = true
	}

	if foundName != "" {
		editDist := minEditDistance(pypireg.NormalizedName(name), pypireg.NormalizedName(foundName))
		verificationResult.levDistance = editDist

		if editDist == 0 {
			verificationResult.nameMatch = true
		}

		verificationResult.foundVersion = foundVersion
		if foundVersion != "" && SameVersion(foundVersion, version) {
			verificationResult.versionMatch = true
		}
	}

	return verificationResult, nil
}

func extractPyProjectRequirements(ctx context.Context, f *git2.File) ([]string, error) {
	var reqs []string
	log.Println("Looking for additional reqs in pyproject.toml")
	pyProject, err := ReadPyProject(f)
	if err != nil {
		return nil, err
	}
	for _, r := range pyProject.BuildSystem.Requires {
		// TODO: Some of these requirements are probably already in rbcfg.Requirements, should we skip
		// them? To even know which package we're looking at would require parsing the dependency spec.
		// https://packaging.python.org/en/latest/specifications/dependency-specifiers/#dependency-specifiers
		reqs = append(reqs, strings.TrimSpace(r))
	}
	// NOTE: scikit-build-core requests dynamic metadata plugin packages from
	// get_requires_for_build_wheel without requiring them in [build-system].requires.
	if strings.HasPrefix(pyProject.BuildSystem.Backend, "scikit_build_core.") {
		for _, field := range slices.Sorted(maps.Keys(pyProject.Tool.ScikitBuild.Metadata)) {
			provider := strings.TrimSpace(pyProject.Tool.ScikitBuild.Metadata[field].Provider)
			if pluginReq, ok := scikitBuildMetadataPlugins[provider]; ok && !HasRequirement(reqs, pluginReq) {
				reqs = append(reqs, pluginReq)
			}
		}
	}
	log.Println("Added these reqs from pyproject.toml: " + strings.Join(reqs, ", "))
	return reqs, nil
}

// RequirementName extracts the normalized distribution name from a dependency
// specifier, dropping extras, version specifiers, direct references and
// markers, so "SetupTools[core]<=67.7.2" yields "setuptools".
// https://packaging.python.org/en/latest/specifications/dependency-specifiers/#grammar
func RequirementName(req string) string {
	fields := strings.FieldsFunc(req, func(r rune) bool { return strings.ContainsRune("=<>~!;[(@ \t", r) })
	if len(fields) == 0 {
		return ""
	}
	return pypireg.NormalizedName(fields[0])
}

// HasRequirement reports whether reqs name any of the packages under any
// specifier. The names must be normalized.
func HasRequirement(reqs []string, names ...string) bool {
	return slices.ContainsFunc(reqs, func(r string) bool { return slices.Contains(names, RequirementName(r)) })
}
