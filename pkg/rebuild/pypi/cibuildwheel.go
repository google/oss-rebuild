// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"maps"
	"path"
	re "regexp"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

const workflowsDir = ".github/workflows"

var (
	// cibuildwheelActionPat matches a step using the cibuildwheel action by a
	// release tag (v2.16.2), floating minor tag (v2.16), or commit hash.
	cibuildwheelActionPat = re.MustCompile(`^pypa/cibuildwheel@v?(\d+\.\d+(?:\.\d+)?|[0-9a-f]{40})$`)
	// cibuildwheelReqPat matches an exact cibuildwheel requirement in a command,
	// such as "pipx run cibuildwheel==2.16.2".
	cibuildwheelReqPat = re.MustCompile(`\bcibuildwheel(?:\[[^\]]*\])?\s*==\s*(\d+\.\d+\.\d+)\b`)
)

// ghaWorkflow holds the parts of a GitHub Actions workflow that can pin the
// cibuildwheel version.
// https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax
type ghaWorkflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// extractCibuildwheelSpecs returns the cibuildwheel version specs (exact
// versions, minor series, or commit hashes) found in the GitHub Actions
// workflows in tree.
func extractCibuildwheelSpecs(tree *object.Tree) ([]string, error) {
	workflows, err := tree.Tree(workflowsDir)
	if errors.Is(err, object.ErrDirectoryNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, errors.Wrapf(err, "finding %s", workflowsDir)
	}
	var specs []string
	for _, entry := range workflows.Entries {
		if ext := path.Ext(entry.Name); !entry.Mode.IsFile() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		f, err := workflows.TreeEntryFile(&entry)
		if err != nil {
			return nil, errors.Wrapf(err, "finding workflow %s", entry.Name)
		}
		contents, err := f.Contents()
		if err != nil {
			return nil, errors.Wrapf(err, "reading workflow %s", entry.Name)
		}
		specs = append(specs, cibuildwheelSpecs([]byte(contents))...)
	}
	return specs, nil
}

func cibuildwheelSpecs(workflow []byte) []string {
	var wf ghaWorkflow
	if err := yaml.Unmarshal(workflow, &wf); err != nil {
		return nil
	}
	var specs []string
	for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
		for _, step := range wf.Jobs[name].Steps {
			if m := cibuildwheelActionPat.FindStringSubmatch(strings.TrimSpace(step.Uses)); m != nil {
				specs = append(specs, m[1])
			}
			for _, m := range cibuildwheelReqPat.FindAllStringSubmatch(step.Run, -1) {
				specs = append(specs, m[1])
			}
		}
	}
	return specs
}

// extractCibuildwheelDependencies extracts system dependencies from [tool.cibuildwheel] and subkeys in pyproject.toml in searchDir.
func extractCibuildwheelDependencies(tree *object.Tree, searchDir string, libc platform.LibcImpl) ([]string, error) {
	if tree == nil {
		return nil, nil
	}
	if searchDir == "" {
		searchDir = "."
	}
	pyprojPath := path.Clean(path.Join(searchDir, "pyproject.toml"))
	f, err := tree.File(pyprojPath)
	if errors.Is(err, object.ErrFileNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Wrapf(err, "finding %s", pyprojPath)
	}
	pyProject, err := parsing.ReadPyProject(f)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", pyprojPath)
	}
	return extractCibuildwheelFromConfig(pyProject.Tool.Cibuildwheel, libc), nil
}

// extractCibuildwheelFromConfig extracts the relevant parts of the cibuildwheel config from the pyproject.toml file.
//
// The relevant keys are taken from https://cibuildwheel.pypa.io/en/stable/configuration/#configuration-file
// for all keys that contain install scripts and are not specific to a non-linux platform.
func extractCibuildwheelFromConfig(cfg parsing.CibuildwheelConfig, libc platform.LibcImpl) []string {
	var scripts []string
	// The cibuildwheel TOML spec allows these fields to be either single strings or arrays of strings.
	scripts = append(scripts, extractStringOrSlice(cfg.BeforeAll)...)
	scripts = append(scripts, extractStringOrSlice(cfg.BeforeBuild)...)
	scripts = append(scripts, extractStringOrSlice(cfg.Linux.BeforeAll)...)
	scripts = append(scripts, extractStringOrSlice(cfg.Linux.BeforeBuild)...)
	for _, ov := range cfg.Overrides {
		if overrideAppliesToTarget(ov.Select, libc) {
			scripts = append(scripts, extractStringOrSlice(ov.BeforeAll)...)
			scripts = append(scripts, extractStringOrSlice(ov.BeforeBuild)...)
		}
	}
	var deps []string
	for _, script := range scripts {
		deps = appendUnique(deps, parsePackageManagerCommands(script, libc)...)
	}
	return deps
}

// extractStringOrSlice extracts data from an individual string or a slice of data that can be cast to a string.
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

func overrideAppliesToTarget(selectVal any, libc platform.LibcImpl) bool {
	selectors := extractStringOrSlice(selectVal)
	if len(selectors) == 0 {
		return true
	}
	for _, sel := range selectors {
		for _, pat := range strings.Fields(strings.ToLower(sel)) {
			if selectorPatternApplies(pat, libc) {
				return true
			}
		}
	}
	return false
}

func selectorPatternApplies(pat string, libc platform.LibcImpl) bool {
	if isNonLinuxPlatformSelector(pat) || isNonX8664ArchSelector(pat) {
		return false
	}
	if libc == platform.Glibc && strings.Contains(pat, "musllinux") && !strings.Contains(pat, "manylinux") {
		return false
	}
	if libc == platform.Musl && strings.Contains(pat, "manylinux") && !strings.Contains(pat, "musllinux") {
		return false
	}
	return true
}

func isNonLinuxPlatformSelector(pat string) bool {
	for _, nonLinux := range []string{"macos", "macosx", "win32", "win_amd64", "win_arm64", "windows", "-ios", "ios_", "pyodide"} {
		if strings.Contains(pat, nonLinux) {
			return true
		}
	}
	return false
}

func isNonX8664ArchSelector(pat string) bool {
	if strings.Contains(pat, "x86_64") || strings.Contains(pat, "amd64") {
		return false
	}
	for _, arch := range []string{"i686", "aarch64", "arm64", "armv7l", "ppc64le", "s390x", "riscv64"} {
		if strings.Contains(pat, arch) {
			return true
		}
	}
	return false
}
