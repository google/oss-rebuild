// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	re "regexp"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	pypiresolver "github.com/google/oss-rebuild/pkg/parsing/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pkg/errors"
)

// These are commonly used in PyPi metadata to point to the project git repo, using a map as a set.
// Some people capitalize these differently, or add/remove spaces. We normalized to lower, no space.
// This list is ordered, we will choose the first occurrence.
var commonRepoLinks = []string{
	"source",
	"sourcecode",
	"repository",
	"project",
	"github",
}

// There are two places to find the repo:
// 1. In the ProjectURLs (project links)
// 2. Embedded in the description
//
// For 1, there are some ProjectURLs that are very common to use for a repo
// (commonRepoLinks above), so we can break up the ProjectURLs

// Preference:
// where               | known repo host
// -------------------------------------
// project source link | yes
// project source link | no
// "Homepage" link     | yes
// description         | yes
// other project links | yes

func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	project, err := mux.PyPI.Project(ctx, t.Package)
	if err != nil {
		return "", nil
	}
	var linksNamedSource []string
	for _, commonName := range commonRepoLinks {
		for name, url := range project.ProjectURLs {
			if strings.ReplaceAll(strings.ToLower(name), " ", "") == commonName {
				linksNamedSource = append(linksNamedSource, url)
				break
			}
		}
	}
	// Four priority levels:
	// 1. link name is common source link name and it points to a known repo host
	// 1.a prefer "Homepage" if it's a common repo host.
	if repo := uri.FindCommonRepo(project.Homepage); repo != "" {
		return uri.CanonicalizeRepoURI(repo)
	}
	for name, url := range project.ProjectURLs {
		if strings.ReplaceAll(strings.ToLower(name), " ", "") == "homepage" {
			if repo := uri.FindCommonRepo(url); repo != "" {
				return uri.CanonicalizeRepoURI(repo)
			}
		}
	}
	// 1.b use other source links.
	for _, url := range linksNamedSource {
		if repo := uri.FindCommonRepo(url); repo != "" {
			return uri.CanonicalizeRepoURI(repo)
		}
	}
	// 2. link name is common source link name but it doesn't point to a known repo host
	if len(linksNamedSource) != 0 {
		return uri.CanonicalizeRepoURI(linksNamedSource[0])
	}
	// 3. first known repo host link found in the description
	r := uri.FindCommonRepo(project.Description)
	// TODO: Maybe revisit this sponsors logic?
	if r != "" && !strings.Contains(r, "sponsors") {
		return uri.CanonicalizeRepoURI(r)
	}
	// 4. link name is not a common source link name, but points to known repo repo host
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

func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, ropt *gitx.RepositoryOptions) (r rebuild.RepoConfig, err error) {
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, ropt.Storer, ropt.Worktree, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
		return r, nil
	case transport.ErrAuthenticationRequired:
		return r, errors.Errorf("repo invalid or private [repo=%s]", r.URI)
	default:
		return r, errors.Wrapf(err, "clone failed [repo=%s]", r.URI)
	}
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
	// Determine setuptools version.
	if slices.ContainsFunc(reqs, func(s string) bool { return strings.HasPrefix(s, "setuptools==") }) {
		// setuptools already set.
		return reqs, nil
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
	release, err := mux.PyPI.Release(ctx, name, version)
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
	r, err := mux.PyPI.Artifact(ctx, name, version, a.Filename)
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
		if buildReqs, err := pypiresolver.ExtractAllRequirements(ctx, tree, name, version); err != nil {
			log.Println(errors.Wrap(err, "Failed to extract reqs from pyproject.toml."))
		} else {
			existing := make(map[string]bool)
			pkgname := func(req string) string {
				return strings.FieldsFunc(req, func(r rune) bool { return strings.ContainsRune("=<>~! \t", r) })[0]
			}
			for _, req := range reqs {
				existing[pkgname(req)] = true
			}
			for _, newReq := range buildReqs {
				if pkg := pkgname(newReq); !existing[pkg] {
					reqs = append(reqs, newReq)
				}
			}
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
var setuptoolsPat = re.MustCompile(`^Generator: setuptools \(([\d\.]+)\)`)
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
			} else if matches := setuptoolsPat.FindSubmatch(line); matches != nil {
				return []string{"setuptools==" + string(matches[1])}, nil
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
	return nil, fs.ErrNotExist
}
