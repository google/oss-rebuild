// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	re "regexp"
	"strings"

	billy "github.com/go-git/go-billy/v5"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/pkg/errors"
)

// These are commonly used in PyPi metadata to point to the project git repo, using a map as a set.
// Some people capitalize these differently, or add/remove spaces. We normalized to lower, no space.
// This list is ordered, we will choose the first occurance.
var commonRepoLinks = []string{
	"source",
	"sourcecode",
	"repository",
	"project",
	"github",
}

// There are two places to find the repo:
// 1. In the ProjectURLs (project links)
// 2. Embeded in the description
//
// For 1, there are some ProjectURLs that are very common to use for a repo
// (commonRepoLinks above), so we can break up the ProjectURLs

// Preference:
// where               | known repo
// --------------------------------
// project source link | yes
// project source link | no
// description         | yes
// other project links | yes

func (Rebuilder) InferRepo(t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	project, err := mux.PyPI.Project(t.Package)
	if err != nil {
		return "", nil
	}
	var repoLinks []string
	for name, url := range project.ProjectURLs {
		for _, commonName := range commonRepoLinks {
			if strings.ToLower(name) == commonName {
				repoLinks = append(repoLinks, url)
				break
			}
		}
	}
	// Four priority levels:
	// 1. project source link, known repo
	for _, url := range repoLinks {
		if repo := uri.FindCommonRepo(url); repo != "" {
			return uri.CanonicalizeRepoURI(repo)
		}
	}
	// 2. project source link, unknown repo
	if len(repoLinks) != 0 {
		return uri.CanonicalizeRepoURI(repoLinks[0])
	}
	// 3. description, known repo
	r := uri.FindCommonRepo(project.Description)
	if r != "" && !strings.Contains(r, "sponsors") {
		return uri.CanonicalizeRepoURI(r)
	}
	// 4. other project links, known repo
	for _, url := range project.ProjectURLs {
		if strings.Contains(url, "sponsors") {
			continue
		}
		if repo := uri.FindCommonRepo(url); repo != "" {
			return uri.CanonicalizeRepoURI(repo)
		}
	}
	return "", errors.New("no git repo")
}

func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, fs billy.Filesystem, s storage.Storer) (r rebuild.RepoConfig, err error) {
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, s, fs, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
	case transport.ErrAuthenticationRequired:
		err = errors.Errorf("repo invalid or private")
		return
	default:
		err = errors.Wrapf(err, "Clone failed [repo=%s]", r.URI)
		return
	}
	return
}

func extractPyProjectRequirements(ctx context.Context, tree *object.Tree) ([]string, error) {
	var reqs []string
	log.Println("Looking for additional reqs in pyproject.toml")
	// TODO: Maybe look for pyproject.toml in subdir?
	f, err := tree.File("pyproject.toml")
	if err != nil {
		return nil, errors.Wrap(err, "Failed to find pyproject.toml")
	}
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

func findGitRef(pkg string, version string, rcfg *rebuild.RepoConfig) (string, error) {
	tagHeuristic, err := rebuild.FindTagMatch(pkg, version, rcfg.Repository)
	log.Printf("Version: %s, tag hash: \"%s\"", version, tagHeuristic)
	if err != nil {
		return "", errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	// TODO: Look for the project.toml and check for version number.
	if tagHeuristic == "" {
		return "", errors.New("no git ref")
	}
	_, err = rcfg.Repository.CommitObject(plumbing.NewHash(tagHeuristic))
	if err != nil {
		switch err {
		case plumbing.ErrObjectNotFound:
			return "", errors.Errorf("[INTERNAL] Commit ref from tag heuristic not found in repo [repo=%s,ref=%s]", rcfg.URI, tagHeuristic)
		default:
			return "", errors.Wrapf(err, "Checkout failed [repo=%s,ref=%s]", rcfg.URI, tagHeuristic)
		}
	}
	return tagHeuristic, nil
}

// FindPureWheel returns the pure wheel artifact from the given version's releases.
func FindPureWheel(artifacts []pypireg.Artifact) (*pypireg.Artifact, error) {
	for _, r := range artifacts {
		if strings.HasSuffix(r.Filename, "none-any.whl") {
			return &r, nil
		}
	}
	return nil, fs.ErrNotExist
}

func inferRequirements(name, version string, zr *zip.Reader) ([]string, error) {
	// Name and version have "-" replaced with "_". See https://packaging.python.org/en/latest/specifications/recording-installed-packages/#the-dist-info-directory
	// TODO: Search for dist-info in the gzip using a regex. It sounds like many tools do varying amounts of normalization on the path name.
	wheelPath := fmt.Sprintf("%s-%s.dist-info/WHEEL", strings.ReplaceAll(name, "-", "_"), strings.ReplaceAll(version, "-", "_"))
	wheel, err := getFile(wheelPath, zr)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to extract upstream %s", wheelPath)
	}
	reqs, err := getGenerator(wheel)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to get upstream generator")
	}
	// TODO: Also find this with a regex.
	metadataPath := fmt.Sprintf("%s-%s.dist-info/METADATA", strings.ReplaceAll(name, "-", "_"), strings.ReplaceAll(version, "-", "_"))
	metadata, err := getFile(metadataPath, zr)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to extract upstream dist-info/METADATA")
	}
	switch {
	case !bytes.Contains(metadata, []byte("License-File")):
		// The License-File value was introduced in later versions so this is the
		// most recent version it could be.
		reqs = append(reqs, "setuptools==56.2.0")
	case bytes.Contains(metadata, []byte("Platform: UNKNOWN")):
		// In later versions, unknown platform is omitted. If we see this pattern, it's an older version
		// of setup tools.
		// TODO: There's probably a more specific version where this behavior changed. I just chose the
		// first version I found that worked.
		reqs = append(reqs, "setuptools==57.5.0")
	default:
		reqs = append(reqs, "setuptools==67.7.2")
	}
	return reqs, nil
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	name, version := t.Package, t.Version
	release, err := mux.PyPI.Release(name, version)
	if err != nil {
		return nil, err
	}
	// TODO: support different build types.
	cfg := &PureWheelBuild{}
	var ref, dir string
	lh, ok := hint.(*rebuild.LocationHint)
	if hint != nil && !ok {
		return nil, errors.Errorf("unsupported hint type: %T", hint)
	}
	if lh != nil && lh.Ref != "" {
		ref = lh.Ref
		if lh.Dir != "" {
			dir = lh.Dir
		} else {
			dir = rcfg.Dir
		}
	} else {
		ref, err = findGitRef(release.Name, version, rcfg)
		if err != nil {
			return cfg, err
		}
		dir = rcfg.Dir
	}
	a, err := FindPureWheel(release.Artifacts)
	if err != nil {
		return cfg, errors.Wrap(err, "finding pure wheel")
	}
	log.Printf("Downloading artifact: %s", a.URL)
	r, err := mux.PyPI.Artifact(name, version, a.Filename)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to read upstream artifact")
	}
	zr, err := zip.NewReader(bytes.NewReader(body), a.Size)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to initialize upstream zip reader")
	}
	reqs, err := inferRequirements(release.Name, version, zr)
	if err != nil {
		return cfg, err
	}
	// Extract pyproject.toml requirements.
	{
		commit, err := rcfg.Repository.CommitObject(plumbing.NewHash(ref))
		if err != nil {
			return cfg, errors.Wrapf(err, "Failed to get commit object")
		}
		tree, err := commit.Tree()
		if err != nil {
			return cfg, errors.Wrapf(err, "Failed to get tree")
		}
		if pyprojReqs, err := extractPyProjectRequirements(ctx, tree); err != nil {
			log.Println(errors.Wrap(err, "Failed to extract reqs from pyproject.toml."))
		} else {
			reqs = append(reqs, pyprojReqs...)
		}
	}
	return &PureWheelBuild{
		Location: rebuild.Location{
			Repo: rcfg.URI,
			Dir:  dir,
			Ref:  ref,
		},
		Requirements: reqs,
	}, nil
}

var bdistWheelPat = re.MustCompile(`^Generator: bdist_wheel \(([\d\.]+)\)`)
var flitPat = re.MustCompile(`^Generator: flit ([\d\.]+)`)
var hatchlingPat = re.MustCompile(`^Generator: hatchling ([\d\.]+)`)

// poetry-core is a subset of poetry. We can treat them as different builders.
var poetryPat = re.MustCompile(`^Generator: poetry ([\d\.]+)`)
var poetryCorePat = re.MustCompile(`^Generator: poetry-core ([\d\.]+)`)

func getGenerator(wheel []byte) (reqs []string, err error) {
	var eol int
	for i := 0; i < len(wheel); i = eol + 1 {
		eol = bytes.IndexRune(wheel[i:], '\n')
		line := wheel[i : i+eol+1]
		sep := bytes.IndexRune(line, ':')
		if sep == -1 {
			// Each line in a WHEEL file has a `key: value` format.
			return nil, errors.New("Unexpected file format")
		}
		key, value := line[:sep], bytes.TrimSpace(line[sep:])
		if bytes.Equal(key, []byte("Generator")) {
			if matches := bdistWheelPat.FindSubmatch(line); matches != nil {
				return []string{"wheel==" + string(matches[1])}, nil
			} else if matches := flitPat.FindSubmatch(line); matches != nil {
				return []string{"flit_core==" + string(matches[1]), "flit==" + string(matches[1])}, nil
			} else if matches := hatchlingPat.FindSubmatch(line); matches != nil {
				return []string{"hatchling==" + string(matches[1])}, nil
			} else if matches := poetryPat.FindSubmatch(line); matches != nil {
				return []string{"poetry==" + string(matches[1])}, nil
			} else if matches := poetryCorePat.FindSubmatch(line); matches != nil {
				return []string{"poetry-core==" + string(matches[1])}, nil
			} else {
				return nil, errors.Errorf("unsupported generator: %s", value)
			}
		}
	}
	return nil, errors.New("no generator found")
}

func getFile(fname string, zr *zip.Reader) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == fname {
			fi, err := zr.Open(f.Name)
			if err != nil {
				return nil, err
			}
			return io.ReadAll(fi)
		}
	}
	return nil, os.ErrNotExist
}
