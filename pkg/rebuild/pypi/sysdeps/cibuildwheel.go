// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/pkg/errors"
)

// ExtractCibuildwheelDependencies extracts system dependencies from [tool.cibuildwheel] and subkeys in pyproject.toml in searchDir.
func ExtractCibuildwheelDependencies(ctx context.Context, tree *object.Tree, searchDir string) ([]DependencyIdentifier, error) {
	if tree == nil {
		return nil, nil
	}
	if searchDir == "" {
		searchDir = "."
	}
	pyprojPath := filepath.Clean(filepath.Join(searchDir, "pyproject.toml"))
	f, err := tree.File(pyprojPath)
	if err == object.ErrFileNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Wrapf(err, "finding %s", pyprojPath)
	}
	pyProject, err := parsing.ReadPyProject(f)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", pyprojPath)
	}
	return extractCibuildwheelFromConfig(pyProject.Tool.Cibuildwheel, pyprojPath+":tool.cibuildwheel"), nil
}

// extractCibuildwheelFromConfig extracts the relevant parts of the cibuildwheel config from the pyproject.toml file
//
// The relevant keys are taken from https://cibuildwheel.pypa.io/en/stable/configuration/#configuration-file
// for all keys that contain install scripts and are not specific to a non-linux platform.
func extractCibuildwheelFromConfig(cfg parsing.CibuildwheelConfig, provenance string) []DependencyIdentifier {
	var scripts []string
	// The cibuildwheel toml spec allows these fields to either be single strings or arrays of
	// strings, so we always have to check
	scripts = append(scripts, extractStringOrSlice(cfg.BeforeAll)...)
	scripts = append(scripts, extractStringOrSlice(cfg.BeforeBuild)...)
	scripts = append(scripts, extractStringOrSlice(cfg.Linux.BeforeAll)...)
	scripts = append(scripts, extractStringOrSlice(cfg.Linux.BeforeBuild)...)
	for _, ov := range cfg.Overrides {
		if overrideAppliesToLinux(ov.Select) {
			scripts = append(scripts, extractStringOrSlice(ov.BeforeAll)...)
			scripts = append(scripts, extractStringOrSlice(ov.BeforeBuild)...)
		}
	}
	var ids []DependencyIdentifier
	for _, script := range scripts {
		ids = append(ids, ParsePackageManagerCommands(script, provenance)...)
	}
	return DeduplicateIdentifiers(ids)
}

// extractStringOrSlice extracts data from an individual string or a slice of data that can be cast to a string
//
// The result is always a string slice.
// A single string will be returned as a 1-element slice.
// A slice of strings will be returned as is.
// A slice of other data will be returned as a slice of all elements that could be cast to a
// nonempty string (after trimming whitespace) in the original order. All elements that could not be
// cast are discarded.
func extractStringOrSlice(v any) []string {
	switch val := v.(type) {
	case string:
		if strings.TrimSpace(val) != "" {
			return []string{val}
		}
	case []any:
		var out []string
		for _, item := range val {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		var out []string
		for _, s := range val {
			if strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func overrideAppliesToLinux(selectVal any) bool {
	selectors := extractStringOrSlice(selectVal)
	if len(selectors) == 0 {
		return true
	}
	for _, sel := range selectors {
		lower := strings.ToLower(sel)
		if strings.Contains(lower, "linux") {
			return true
		}
		if !isNonLinuxPlatformSelector(lower) {
			return true
		}
	}
	return false
}

func isNonLinuxPlatformSelector(sel string) bool {
	for _, nonLinux := range []string{"macos", "macosx", "win32", "win_amd64", "win_arm64", "windows", "ios", "pyodide"} {
		if strings.Contains(sel, nonLinux) {
			return true
		}
	}
	return false
}
