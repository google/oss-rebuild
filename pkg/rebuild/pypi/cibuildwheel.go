// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"maps"
	"path"
	re "regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/internal/semver"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// workflowsDir holds the GitHub Actions workflows of a repository.
const workflowsDir = ".github/workflows"

var (
	// cibuildwheelActionPat matches a step that uses the cibuildwheel action by
	// a release tag, such as "pypa/cibuildwheel@v2.16.2", by a floating minor
	// tag, such as "pypa/cibuildwheel@v2.16", or by the full hash of a tagged
	// commit.
	cibuildwheelActionPat = re.MustCompile(`^pypa/cibuildwheel@(?:v?(\d+\.\d+\.\d+)|v?(\d+\.\d+)|([0-9a-f]{40}))$`)
	// cibuildwheelReqPat matches an exact cibuildwheel requirement in a
	// command, such as "pipx run cibuildwheel==2.16.2".
	cibuildwheelReqPat = re.MustCompile(`\bcibuildwheel(?:\[[^\]]*\])?\s*==\s*(\d+\.\d+\.\d+)\b`)
)

// ghaWorkflow holds the parts of a GitHub Actions workflow that can pin the
// cibuildwheel version.
// https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax
type ghaWorkflow struct {
	Jobs map[string]ghaJob `yaml:"jobs"`
}

type ghaJob struct {
	Steps []ghaStep `yaml:"steps"`
}

type ghaStep struct {
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
}

// extractCibuildwheelVersion returns the highest cibuildwheel version that the
// GitHub Actions workflows in tree pinned at the time of the upload, or "" if
// they pinned none. Floating tags and commit-pinned uses of the cibuildwheel
// action are resolved to versions with pins.
func extractCibuildwheelVersion(tree *object.Tree, pins platform.CibuildwheelPinTable, uploaded time.Time) (string, error) {
	workflows, err := tree.Tree(workflowsDir)
	if err == object.ErrDirectoryNotFound {
		return "", nil
	} else if err != nil {
		return "", errors.Wrapf(err, "finding %s", workflowsDir)
	}
	var highest string
	for _, entry := range workflows.Entries {
		if ext := path.Ext(entry.Name); !entry.Mode.IsFile() || ext != ".yml" && ext != ".yaml" {
			continue
		}
		f, err := workflows.TreeEntryFile(&entry)
		if err != nil {
			return "", errors.Wrapf(err, "finding workflow %s", entry.Name)
		}
		contents, err := f.Contents()
		if err != nil {
			return "", errors.Wrapf(err, "reading workflow %s", entry.Name)
		}
		for _, v := range cibuildwheelVersions([]byte(contents), pins, uploaded) {
			if highest == "" || semver.Cmp(v, highest) > 0 {
				highest = v
			}
		}
	}
	return highest, nil
}

// cibuildwheelVersions returns the cibuildwheel versions that a GitHub Actions
// workflow pinned at the time of the upload, either as the release of the
// cibuildwheel action that a step uses or as an exact requirement in a command
// that a step runs. Versions are ordered by job name. A workflow that cannot
// be parsed pins none.
func cibuildwheelVersions(workflow []byte, pins platform.CibuildwheelPinTable, uploaded time.Time) []string {
	var wf ghaWorkflow
	if err := yaml.Unmarshal(workflow, &wf); err != nil {
		return nil
	}
	var versions []string
	for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
		for _, step := range wf.Jobs[name].Steps {
			if v, ok := actionVersion(step.Uses, pins, uploaded); ok {
				versions = append(versions, v)
			}
			for _, m := range cibuildwheelReqPat.FindAllStringSubmatch(step.Run, -1) {
				versions = append(versions, m[1])
			}
		}
	}
	return versions
}

// actionVersion returns the release of the cibuildwheel action that a step
// used at the time of the upload. A release tag names the version. A floating
// minor tag resolves to the latest release of its series that was published
// by then. A commit resolves to the release tagged at it. ok is false if the
// step uses another action, a branch, a series without a release by then or a
// commit that no release tag in pins points to.
func actionVersion(uses string, pins platform.CibuildwheelPinTable, uploaded time.Time) (version string, ok bool) {
	m := cibuildwheelActionPat.FindStringSubmatch(strings.TrimSpace(uses))
	switch {
	case m == nil:
		return "", false
	case m[1] != "":
		return m[1], true
	case m[2] != "":
		// NOTE: The upload time stands in for the build time, which PyPI does
		// not record. A release of the series published between the build and
		// the upload is picked although the build ran the previous one.
		return pins.LatestInSeries(m[2], uploaded)
	default:
		return pins.VersionTaggedAt(m[3])
	}
}
