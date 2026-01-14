// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

type projectMetadata struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

type toolMetadata struct {
	Poetry projectMetadata `toml:"poetry"`
}

type pyProjectProject struct {
	Metadata projectMetadata `toml:"project"`
	Tool     toolMetadata    `toml:"tool"`
}

func verifyPyProjectFile(ctx context.Context, foundFile foundFile, name, version string) (fileVerification, error) {
	var verificationResult fileVerification
	verificationResult.foundF = foundFile
	f := foundFile.object

	pyprojContents, err := f.Contents()
	if err != nil {
		return verificationResult, errors.Wrap(err, "Failed to read pyproject.toml")
	}
	var pyProject pyProjectProject
	if err := toml.Unmarshal([]byte(pyprojContents), &pyProject); err != nil {
		return verificationResult, errors.Wrap(err, "Failed to decode pyproject.toml")
	}
	foundName := ""
	foundVersion := ""
	if pyProject.Metadata.Name != "" {
		foundName = pyProject.Metadata.Name
		foundVersion = pyProject.Metadata.Version
	} else if pyProject.Tool.Poetry.Name != "" {
		foundName = pyProject.Tool.Poetry.Name
		foundVersion = pyProject.Tool.Poetry.Version
	}

	if foundFile.path == "." {
		verificationResult.main = true
	}

	if foundName != "" {
		editDist := minEditDistance(normalizeName(name), normalizeName(foundName))
		verificationResult.levDistance = editDist

		if editDist == 0 {
			verificationResult.nameMatch = true
		}

		if foundVersion != "" && version == foundVersion {
			verificationResult.versionMatch = true
		}
	}

	return verificationResult, nil
}

func extractPyProjectRequirements(ctx context.Context, f *object.File) ([]string, error) {
	var reqs []string
	log.Println("Looking for additional reqs in pyproject.toml")
	pyprojContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read pyproject.toml")
	}
	type BuildSystem struct {
		Requirements []string `toml:"requires"`
	}
	type PyProject struct {
		Build BuildSystem `toml:"build-system"`
	}
	var pyProject PyProject
	if err := toml.Unmarshal([]byte(pyprojContents), &pyProject); err != nil {
		return nil, errors.Wrap(err, "Failed to decode pyproject.toml")
	}
	for _, r := range pyProject.Build.Requirements {
		// TODO: Some of these requirements are probably already in rbcfg.Requirements, should we skip
		// them? To even know which package we're looking at would require parsing the dependency spec.
		// https://packaging.python.org/en/latest/specifications/dependency-specifiers/#dependency-specifiers
		reqs = append(reqs, strings.ReplaceAll(r, " ", ""))
	}
	log.Println("Added these reqs from pyproject.toml: " + strings.Join(reqs, ", "))
	return reqs, nil
}
