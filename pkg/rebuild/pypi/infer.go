// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	re "regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/pelletier/go-toml/v2"
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

func (Rebuilder) CloneRepo(ctx context.Context, t rebuild.Target, repoURI string, fs billy.Filesystem, s storage.Storer) (r rebuild.RepoConfig, err error) {
	r.URI = repoURI
	r.Repository, err = rebuild.LoadRepo(ctx, t.Package, s, fs, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.DefaultSubmoduleRecursionDepth})
	switch err {
	case nil:
		return r, nil
	case transport.ErrAuthenticationRequired:
		return r, errors.Errorf("repo invalid or private [repo=%s]", r.URI)
	default:
		return r, errors.Wrapf(err, "clone failed [repo=%s]", r.URI)
	}
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
		if strings.Contains(r, "python_version") {
			r = strings.Split(r, ";")[0]
		}
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

func FindSourceDistribution(artifacts []pypireg.Artifact) (*pypireg.Artifact, error) {
	for _, r := range artifacts {
		if strings.HasSuffix(r.Filename, ".tar.gz") {
			return &r, nil
		}
	}
	return nil, fs.ErrNotExist
}

func inferRequirements(name, version string, zr interface{}) ([]string, error) {
	// Name and version have "-" replaced with "_". See https://packaging.python.org/en/latest/specifications/recording-installed-packages/#the-dist-info-directory
	// TODO: Search for dist-info in the archive using a regex. It sounds like many tools do varying amounts of normalization on the path name.
	wheelPath := fmt.Sprintf("%s-%s.dist-info/WHEEL", strings.ReplaceAll(name, "-", "_"), strings.ReplaceAll(version, "-", "_"))

	var wheel []byte
	var err error
	var reqs []string

	switch reader := zr.(type) {
	case *zip.Reader:
		wheel, err = getFile(wheelPath, reader)
	case *tar.Reader:
		return reqs, nil
		// wheel, err = getFileFromTarGz(wheelPath, reader)
	default:
		return nil, errors.New("[INTERNAL] Unsupported archive reader type")
	}

	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to extract upstream %s", wheelPath)
	}

	reqs, err = getGenerator(wheel)
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

	var metadata []byte

	switch reader := zr.(type) {
	case *zip.Reader:
		metadata, err = getFile(metadataPath, reader)
	case *tar.Reader:
		metadata, err = getFileFromTarGz(metadataPath, reader)
	default:
		return nil, errors.New("[INTERNAL] Unsupported archive reader type")
	}

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

func inferPythonVersion(artifact pypireg.Artifact) string {
	var pythonVersions []string

	t := artifact.UploadTime
	switch {
	case t.After(time.Date(2023, 10, 2, 0, 0, 0, 0, time.UTC)):
		pythonVersions = append(pythonVersions, "3.12.4") // Python 3.12 release
	case t.After(time.Date(2022, 10, 24, 0, 0, 0, 0, time.UTC)):
		pythonVersions = append(pythonVersions, "3.11.8") // Python 3.11 release
	case t.After(time.Date(2021, 10, 4, 0, 0, 0, 0, time.UTC)):
		pythonVersions = append(pythonVersions, "3.10.13") // Python 3.10 release
	case t.After(time.Date(2020, 10, 5, 0, 0, 0, 0, time.UTC)):
		pythonVersions = append(pythonVersions, "3.9.18") // Python 3.9 release
	case t.After(time.Date(2019, 10, 14, 0, 0, 0, 0, time.UTC)):
		pythonVersions = append(pythonVersions, "3.8.18") // Python 3.8 release
	case t.After(time.Date(2018, 6, 27, 0, 0, 0, 0, time.UTC)):
		pythonVersions = append(pythonVersions, "3.7.17") // Python 3.7 release
	case t.After(time.Date(2014, 3, 16, 0, 0, 0, 0, time.UTC)):
		pythonVersions = append(pythonVersions, "3.6.15") // Python 3.6 release

	// Those are exluded until a proper way to support older python distributions
	// case t.After(time.Date(2016, 12, 23, 0, 0, 0, 0, time.UTC)):
	// 	pythonVersions = append(pythonVersions, "3.6.15") // Python 3.6 release
	// case t.After(time.Date(2015, 9, 13, 0, 0, 0, 0, time.UTC)):
	// 	pythonVersions = append(pythonVersions, "3.5.10") // Python 3.5 release
	// case t.After(time.Date(2014, 3, 16, 0, 0, 0, 0, time.UTC)):
	// 	pythonVersions = append(pythonVersions, "3.4.10") // Python 3.4 release
	default:
		pythonVersions = append(pythonVersions, "3.12.4") // Default to newest supported version
	}

	return pythonVersions[0]
}

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	name, version := t.Package, t.Version
	release, err := mux.PyPI.Release(ctx, name, version)
	if err != nil {
		return nil, err
	}
	// TODO: support different build types.
	cfg := &PureWheelBuild{}
	scfg := &SourceDistributionBuild{}
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
	zipFile := true
	a, err := FindPureWheel(release.Artifacts)
	if err != nil {
		zipFile = false
		a, err = FindSourceDistribution(release.Artifacts)
		if err != nil {
			return scfg, errors.Wrap(err, "Failed to find pure wheel and source distribution")
		}
	}
	pythonVersion := inferPythonVersion(*a)
	// if err != nil {
	// 	return cfg, errors.Wrap(err, "finding pure wheel")
	// }
	log.Printf("Downloading artifact: %s", a.URL)
	r, err := mux.PyPI.Artifact(ctx, name, version, a.Filename)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to read upstream artifact")
	}
	var zr interface{}
	if zipFile {
		zr, err = zip.NewReader(bytes.NewReader(body), a.Size)
		if err != nil {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed to initialize upstream zip reader")
		}
	} else {
		gzf, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed to initialize gzip reader")
		}
		zr = tar.NewReader(gzf)
	}
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
			existing := make(map[string]bool)
			pkgname := func(req string) string {
				return strings.FieldsFunc(req, func(r rune) bool { return strings.ContainsRune("=<>~! \t", r) })[0]
			}
			for _, req := range reqs {
				existing[pkgname(req)] = true
			}
			for _, newReq := range pyprojReqs {
				if pkg := pkgname(newReq); !existing[pkg] {
					reqs = append(reqs, newReq)
				}
			}
		}
	}

	if zipFile {
		return &PureWheelBuild{
			Location: rebuild.Location{
				Repo: rcfg.URI,
				Dir:  dir,
				Ref:  ref,
			},
			Requirements:  reqs,
			PythonVersion: pythonVersion,
		}, nil
	}
	return &SourceDistributionBuild{
		Location: rebuild.Location{
			Repo: rcfg.URI,
			Dir:  dir,
			Ref:  ref,
		},
		Requirements:  reqs,
		PythonVersion: pythonVersion,
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

// getFileFromTarGz extracts a file with the given name from a tar.Reader.
func getFileFromTarGz(fname string, tr *tar.Reader) ([]byte, error) {
	for {
		header, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break // End of archive
			}
			return nil, err
		}

		// Check if the current file matches the requested file name
		if header.Name == fname {
			// Read the file contents
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			return data, nil
		}
	}

	// File not found in the archive
	return nil, fs.ErrNotExist
}
