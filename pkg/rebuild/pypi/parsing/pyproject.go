// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"log"
	"path"
	"path/filepath"
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

type buildSystem struct {
	Requires     []string `toml:"requires"`
	BuildBackend string   `toml:"build-backend"`
	BackendPath  any      `toml:"backend-path"`
}

type pyProjectProject struct {
	Metadata    projectMetadata `toml:"project"`
	Tool        toolMetadata    `toml:"tool"`
	BuildSystem buildSystem     `toml:"build-system"`
}

func verifyPyProjectFile(ctx context.Context, f *object.File, name, version string) (fileVerification, error) {
	var verificationResult fileVerification
	verificationResult.foundF = f

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
	pyprojContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read pyproject.toml")
	}
	var pyProject pyProjectProject
	if err := toml.Unmarshal([]byte(pyprojContents), &pyProject); err != nil {
		return nil, errors.Wrap(err, "Failed to decode pyproject.toml")
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

// InTreeBackendModulePaths returns the source files that may define the
// in-tree PEP 517 backend configured in the pyproject.toml f: the dotted
// build-backend module resolved against every backend-path entry, as either
// a module file or a package __init__.py. Paths are relative to the directory
// containing f. The optional ":object" suffix of build-backend names an
// attribute within the module and is ignored.
// https://peps.python.org/pep-0517/#in-tree-build-backends
func InTreeBackendModulePaths(f *object.File) ([]string, error) {
	pyprojContents, err := f.Contents()
	if err != nil {
		return nil, errors.Wrap(err, "reading pyproject.toml")
	}
	var pyProject pyProjectProject
	if err := toml.Unmarshal([]byte(pyprojContents), &pyProject); err != nil {
		return nil, errors.Wrap(err, "decoding pyproject.toml")
	}
	module, _, _ := strings.Cut(pyProject.BuildSystem.BuildBackend, ":")
	module = strings.TrimSpace(module)
	if module == "" {
		return nil, nil
	}
	modulePath := strings.ReplaceAll(module, ".", "/")
	var candidates []string
	for _, bp := range backendPaths(pyProject.BuildSystem.BackendPath) {
		// NOTE: PEP 517 requires backend-path entries to stay within the source
		// tree, and frontends reject any that do not.
		base := path.Join(bp, modulePath)
		if path.IsAbs(bp) || base == ".." || strings.HasPrefix(base, "../") {
			continue
		}
		candidates = append(candidates, base+".py", path.Join(base, "__init__.py"))
	}
	return candidates, nil
}

// backendPaths returns the non-empty entries of a backend-path list.
func backendPaths(v any) []string {
	items, _ := v.([]any)
	var out []string
	for _, item := range items {
		if s, ok := item.(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}
