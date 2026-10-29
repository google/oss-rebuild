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

// cibuildwheelProjectDir is where cibuildwheel copies the project to in Linux
// build containers.
// https://cibuildwheel.pypa.io/en/stable/platforms/#linux-containers
const cibuildwheelProjectDir = "/project"

var (
	// cibuildwheelActionPat matches a step using the cibuildwheel action by a
	// release tag (v2.16.2), floating minor tag (v2.16), or commit hash.
	cibuildwheelActionPat = re.MustCompile(`^pypa/cibuildwheel@v?(\d+\.\d+(?:\.\d+)?|[0-9a-f]{40})$`)
	// cibuildwheelReqPat matches an exact cibuildwheel requirement in a command,
	// such as "pipx run cibuildwheel==2.16.2".
	cibuildwheelReqPat = re.MustCompile(`\bcibuildwheel(?:\[[^\]]*\])?\s*==\s*(\d+\.\d+\.\d+)\b`)
	// cibuildwheelCmdPat matches a command that names cibuildwheel, such as
	// "pipx run cibuildwheel" or "python -m cibuildwheel".
	cibuildwheelCmdPat = re.MustCompile(`\bcibuildwheel\b`)
)

// ghaWorkflow holds the parts of a GitHub Actions workflow that can run or pin
// cibuildwheel.
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
// workflows in tree, and reports whether any step runs cibuildwheel (including
// unpinned uses).
func extractCibuildwheelSpecs(tree *object.Tree) (specs []string, usesCibuildwheel bool, err error) {
	workflows, err := tree.Tree(workflowsDir)
	if errors.Is(err, object.ErrDirectoryNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, errors.Wrapf(err, "finding %s", workflowsDir)
	}
	for _, entry := range workflows.Entries {
		if ext := path.Ext(entry.Name); !entry.Mode.IsFile() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		f, err := workflows.TreeEntryFile(&entry)
		if err != nil {
			return nil, false, errors.Wrapf(err, "finding workflow %s", entry.Name)
		}
		contents, err := f.Contents()
		if err != nil {
			return nil, false, errors.Wrapf(err, "reading workflow %s", entry.Name)
		}
		s, u := cibuildwheelSpecs([]byte(contents))
		specs = append(specs, s...)
		usesCibuildwheel = usesCibuildwheel || u
	}
	return specs, usesCibuildwheel, nil
}

func cibuildwheelSpecs(workflow []byte) (specs []string, usesCibuildwheel bool) {
	var wf ghaWorkflow
	if err := yaml.Unmarshal(workflow, &wf); err != nil {
		return nil, false
	}
	for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
		for _, step := range wf.Jobs[name].Steps {
			uses := strings.TrimSpace(step.Uses)
			if m := cibuildwheelActionPat.FindStringSubmatch(uses); m != nil {
				specs = append(specs, m[1])
			}
			for _, m := range cibuildwheelReqPat.FindAllStringSubmatch(step.Run, -1) {
				specs = append(specs, m[1])
			}
			if strings.HasPrefix(uses, "pypa/cibuildwheel@") || cibuildwheelCmdPat.MatchString(step.Run) {
				usesCibuildwheel = true
			}
		}
	}
	return specs, usesCibuildwheel
}
