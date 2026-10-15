// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

// ProjectMetadata represents the [project] or [tool.poetry] table in pyproject.toml.
type ProjectMetadata struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

// BuildSystem represents the [build-system] table in pyproject.toml.
type BuildSystem struct {
	Requires []string `toml:"requires"`
}

// CibuildwheelHooks represents build hooks in [tool.cibuildwheel] or platform subtables.
type CibuildwheelHooks struct {
	BeforeAll   any `toml:"before-all"`
	BeforeBuild any `toml:"before-build"`
}

// CibuildwheelOverride represents an entry in [[tool.cibuildwheel.overrides]].
type CibuildwheelOverride struct {
	Select      any `toml:"select"`
	BeforeAll   any `toml:"before-all"`
	BeforeBuild any `toml:"before-build"`
}

// CibuildwheelConfig represents the [tool.cibuildwheel] table in pyproject.toml.
type CibuildwheelConfig struct {
	BeforeAll   any                    `toml:"before-all"`
	BeforeBuild any                    `toml:"before-build"`
	Linux       CibuildwheelHooks      `toml:"linux"`
	Overrides   []CibuildwheelOverride `toml:"overrides"`
}

// ToolConfig represents the [tool] table in pyproject.toml.
type ToolConfig struct {
	Poetry       ProjectMetadata    `toml:"poetry"`
	Cibuildwheel CibuildwheelConfig `toml:"cibuildwheel"`
}

// PyProject represents the structure of a pyproject.toml file.
type PyProject struct {
	Project     ProjectMetadata `toml:"project"`
	BuildSystem BuildSystem     `toml:"build-system"`
	Tool        ToolConfig      `toml:"tool"`
}

// ParsePyProject unmarshals pyproject.toml content into a PyProject struct.
func ParsePyProject(contents string) (PyProject, error) {
	var pyProject PyProject
	if err := toml.Unmarshal([]byte(contents), &pyProject); err != nil {
		return pyProject, errors.Wrap(err, "decoding pyproject.toml")
	}
	return pyProject, nil
}

// ReadPyProject reads and unmarshals a pyproject.toml git object file.
func ReadPyProject(f *object.File) (PyProject, error) {
	contents, err := f.Contents()
	if err != nil {
		return PyProject{}, errors.Wrap(err, "reading pyproject.toml")
	}
	return ParsePyProject(contents)
}

func verifyPyProjectFile(ctx context.Context, f *object.File, name, version string) (fileVerification, error) {
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
		editDist := minEditDistance(normalizeName(name), normalizeName(foundName))
		verificationResult.levDistance = editDist

		if editDist == 0 {
			verificationResult.nameMatch = true
		}

		verificationResult.foundVersion = foundVersion
		if foundVersion != "" && version == foundVersion {
			verificationResult.versionMatch = true
		}
	}

	return verificationResult, nil
}

func extractPyProjectRequirements(ctx context.Context, f *object.File) ([]string, error) {
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
	log.Println("Added these reqs from pyproject.toml: " + strings.Join(reqs, ", "))
	return reqs, nil
}
