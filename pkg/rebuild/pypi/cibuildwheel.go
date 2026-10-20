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
