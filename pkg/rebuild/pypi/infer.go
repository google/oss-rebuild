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
	"path"
	re "regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	pypiresolver "github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	pypireg "github.com/google/oss-rebuild/pkg/registry/pypi"
	"github.com/google/oss-rebuild/pkg/vcs/gitscan"
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

var distInfoFieldPat = re.MustCompile(`[-_.]+`)

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

func validateCommitCandidate(commitCandidate string, rcfg *rebuild.RepoConfig, heuristicName string) (bool, error) {
	if commitCandidate == "" {
		return false, nil
	}
	_, err := rcfg.Repository.CommitObject(plumbing.NewHash(commitCandidate))
	if err != nil {
		switch err {
		case plumbing.ErrObjectNotFound:
			return false, errors.Errorf("[INTERNAL] Commit ref from %s heuristic not found in repo [repo=%s,ref=%s]", heuristicName, rcfg.URI, commitCandidate)
		default:
			return false, errors.Wrapf(err, "Checkout failed [repo=%s,ref=%s]", rcfg.URI, commitCandidate)
		}
	}
	return true, nil
}

func findGitRef(ctx context.Context, target rebuild.Target, mux rebuild.RegistryMux, pkg string, version string, rcfg *rebuild.RepoConfig) (string, error) {
	tagHeuristic, err := rebuild.FindTagMatch(pkg, version, rcfg.Repository)
	log.Printf("Version: %s, tag hash: \"%s\"", version, tagHeuristic)
	if err != nil {
		return "", errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	valid, err := validateCommitCandidate(tagHeuristic, rcfg, "tag")
	if err != nil {
		return "", err
	}
	if valid {
		log.Printf("PyPI Returning result from tag heuristic.")
		return tagHeuristic, nil
	}

	closestCommit, err := findClosestCommitToSource(ctx, target, mux, rcfg.Repository)
	if err != nil {
		return "", errors.Wrapf(err, "[INTERNAL] commit overlap heuristic error")
	}
	commitHeuristic := closestCommit.Hash.String()
	valid, err = validateCommitCandidate(commitHeuristic, rcfg, "commit")
	if err != nil {
		return "", err
	}
	if valid {
		log.Printf("PyPI Returning result from commit heuristic.")
		return commitHeuristic, nil
	}
	return "", errors.New("no git ref")
}

func findClosestCommitToSource(ctx context.Context, target rebuild.Target, mux rebuild.RegistryMux, repo *git.Repository) (*object.Commit, error) {
	sourceArtifact, err := mux.PyPI.Artifact(ctx, target.Package, target.Version, target.Artifact)
	if err != nil {
		return nil, err
	}
	defer sourceArtifact.Close()

	var hashes []plumbing.Hash
	if strings.HasSuffix(target.Artifact, ".tar.gz") {
		gzReader, err := gzip.NewReader(sourceArtifact)
		if err != nil {
			return nil, err
		}
		defer gzReader.Close()
		tarData, err := io.ReadAll(gzReader)
		if err != nil {
			return nil, err
		}
		tarReader := tar.NewReader(bytes.NewReader(tarData))
		hashes, err = gitscan.BlobHashesFromTar(tarReader)
		if err != nil {
			return nil, errors.Wrap(err, "hashing source tar contents")
		}
	} else if strings.HasSuffix(target.Artifact, ".whl") {
		zipData, err := io.ReadAll(sourceArtifact)
		if err != nil {
			return nil, err
		}
		zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
		if err != nil {
			return nil, err
		}
		hashes, err = gitscan.BlobHashesFromZip(zipReader)
		if err != nil {
			return nil, errors.Wrap(err, "hashing source zip contents")
		}
	} else {
		return nil, errors.Errorf("Incompatible release type: %s", target.Artifact)
	}

	return gitscan.FindClosestCommitToSource(ctx, repo, hashes)
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

func FindSourceDist(artifacts []pypireg.Artifact) (*pypireg.Artifact, error) {
	for _, r := range artifacts {
		if strings.HasSuffix(r.Filename, ".tar.gz") {
			return &r, nil
		}
	}
	return nil, fs.ErrNotExist
}

func inferRequirements(name, version string, zr *zip.Reader) ([]string, error) {
	distInfoDir, err := getDistInfoDir(name, version, zr)
	if err != nil {
		wheelPath := path.Join(expectedDistInfoDir(name, version), "WHEEL")
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to extract upstream %s", wheelPath)
	}
	wheelPath := path.Join(distInfoDir, "WHEEL")
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
	metadataPath := path.Join(distInfoDir, "METADATA")
	metadata, err := getFile(metadataPath, zr)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to extract upstream %s", metadataPath)
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

// Wheel dist-info names use escaped distribution/version components:
// https://packaging.python.org/en/latest/specifications/binary-distribution-format/#escaping-and-unicode
// Name comparisons use PyPA name normalization:
// https://packaging.python.org/en/latest/specifications/name-normalization/
func normalizeDistInfoName(name string) string {
	normalized := distInfoFieldPat.ReplaceAllString(name, "-")
	return strings.ReplaceAll(strings.ToLower(normalized), "-", "_")
}

func normalizeDistInfoVersion(version string) string {
	return strings.ReplaceAll(strings.ToLower(version), "-", "_")
}

func expectedDistInfoDir(name, version string) string {
	return fmt.Sprintf("%s-%s.dist-info", normalizeDistInfoName(name), normalizeDistInfoVersion(version))
}

func getDistInfoDir(name, version string, zr *zip.Reader) (string, error) {
	expectedDir := expectedDistInfoDir(name, version)
	if hasZipDir(expectedDir, zr) {
		return expectedDir, nil
	}
	// Older wheels may use equivalent but unescaped names with uppercase letters
	// or "." separators; the wheel spec requires consumers to accept them.
	for _, f := range zr.File {
		dir := path.Dir(f.Name)
		if dir == "." || path.Dir(dir) != "." {
			continue
		}
		stem, ok := strings.CutSuffix(dir, ".dist-info")
		if !ok {
			continue
		}
		dash := strings.LastIndexByte(stem, '-')
		if dash == -1 {
			continue
		}
		foundName, foundVersion := stem[:dash], stem[dash+1:]
		if normalizeDistInfoName(foundName) != normalizeDistInfoName(name) {
			continue
		}
		if normalizeDistInfoVersion(foundVersion) != normalizeDistInfoVersion(version) {
			continue
		}
		return dir, nil
	}
	return "", fs.ErrNotExist
}

func hasZipDir(dir string, zr *zip.Reader) bool {
	prefix := dir + "/"
	for _, f := range zr.File {
		if f.Name == dir || strings.HasPrefix(f.Name, prefix) {
			return true
		}
	}
	return false
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
	var a *pypireg.Artifact
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
		ref, err = findGitRef(ctx, t, mux, release.Name, version, rcfg)
		if err != nil {
			return cfg, err
		}
		dir = rcfg.Dir
	}

	for _, art := range release.Artifacts {
		if art.Filename == t.Artifact {
			a = &art
			break
		}
	}
	if a == nil {
		return cfg, errors.Errorf("artifact %s not found in release", t.Artifact)
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
	var reqs []string
	if strings.HasSuffix(a.Filename, ".whl") {
		zr, err := zip.NewReader(bytes.NewReader(body), a.Size)
		if err != nil {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed to initialize upstream zip reader")
		}
		reqs, err = inferRequirements(release.Name, version, zr)
		if err != nil {
			return cfg, err
		}
	} else if strings.HasSuffix(a.Filename, ".tar.gz") {
		// For .tar.gz files (source distributions), we don't infer requirements from the archive
		// We'll get them from pyproject.toml below
		reqs = []string{}
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
		newFoundDir, err := pypiresolver.DiscoverBuildDir(ctx, tree, name, version, dir)
		if err != nil {
			log.Println(errors.Wrap(err, "Failed to discover build dir."))
		} else {
			// NOTE - This should NOT overwrite the hint dir if one exists, but utilize it and return it again
			//   Test "pyproject.toml - Detect package with dir hint" showcases this
			dir = newFoundDir
		}
		if buildReqs, err := pypiresolver.ExtractRequirements(ctx, tree, dir); err != nil {
			log.Println(errors.Wrap(err, "Failed to extract reqs from build files."))
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
	if strings.HasSuffix(a.Filename, ".tar.gz") {
		return &SdistBuild{
			Location: rebuild.Location{
				Repo: rcfg.URI,
				Dir:  dir,
				Ref:  ref,
			},
			PythonVersion: inferPythonVersion(reqs),
			Requirements:  reqs,
			RegistryTime:  a.UploadTime,
		}, nil
	} else {
		return &PureWheelBuild{
			Location: rebuild.Location{
				Repo: rcfg.URI,
				Dir:  dir,
				Ref:  ref,
			},
			PythonVersion: inferPythonVersion(reqs),
			Requirements:  reqs,
			RegistryTime:  a.UploadTime,
		}, nil
	}
}

func inferPythonVersion(reqs []string) string {
	constraintPat := re.MustCompile(`([<>=!~]+)\s*(\d+)`)
	for _, req := range reqs {
		parts := strings.FieldsFunc(req, func(r rune) bool { return strings.ContainsRune("=<>~! \t[", r) })
		if len(parts) == 0 || strings.ToLower(parts[0]) != "setuptools" {
			continue
		}
		allConstraints := constraintPat.FindAllStringSubmatch(req, -1)
		for _, matches := range allConstraints {
			op := matches[1]
			ver, err := strconv.Atoi(matches[2])
			if err != nil {
				continue
			}
			switch op {
			case "<":
				if ver <= 60 {
					return "3.11"
				}
			case "<=", "==":
				if ver < 60 {
					return "3.11"
				}
			}
		}
	}
	return "" // unconstrained
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
		key, value := line[:sep], bytes.TrimSpace(line[sep+1:])
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
