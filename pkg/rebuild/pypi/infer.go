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
	"path"
	re "regexp"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/internal/versionx"
	pypiresolver "github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
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
		return "", errors.Wrap(err, "fetching pypi metadata")
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
	repo, err := rebuild.LoadRepo(ctx, t.Package, ropt.Storer, ropt.Worktree, git.CloneOptions{URL: r.URI, RecurseSubmodules: git.NoRecurseSubmodules, NoCheckout: true})
	switch err {
	case nil:
		r.Repo = *repo
		return r, nil
	case transport.ErrAuthenticationRequired:
		return r, errors.Errorf("repo invalid or private [repo=%s]", r.URI)
	default:
		return r, errors.Wrapf(err, "clone failed [repo=%s]", r.URI)
	}
}

// findGitRef resolves the commit a release was built from: a tag naming the
// version when one exists, else the commit whose tree matches the pure wheel
// file blobs. A fallback that errors internally is logged and skipped, never
// aborting the chain.
func findGitRef(ctx context.Context, mux rebuild.RegistryMux, pkg, version string, release *pypireg.Release, rcfg *rebuild.RepoConfig) (string, error) {
	tagHeuristic, err := rebuild.FindTagMatch(pkg, version, rcfg.Repository)
	if err != nil {
		return "", errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	log.Printf("Version: %s, tag hash: \"%s\"", version, tagHeuristic)
	if tagHeuristic != "" {
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
	if ref, err := archiveContentRef(ctx, mux, pkg, version, release, rcfg.Repository); err != nil {
		log.Printf("archive-content search failed [pkg=%s,ver=%s]: %v", pkg, version, err)
	} else if ref != "" {
		log.Printf("using archive-content ref: %s", shortHash(ref))
		return ref, nil
	}
	return "", errors.New("no git ref")
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
	metadataPath := path.Join(distInfoDir, "METADATA")
	metadata, err := getFile(metadataPath, zr)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to extract upstream %s", metadataPath)
	}
	reqs, err := getGenerator(wheel, metadata)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] Failed to get upstream generator")
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

// requirementName extracts the distribution name from a PEP 508 requirement,
// dropping extras, version specifiers and markers, so "setuptools[core]<=67.7.2"
// yields "setuptools".
func requirementName(req string) string {
	fields := strings.FieldsFunc(req, func(r rune) bool { return strings.ContainsRune("=<>~!;[ \t", r) })
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// mergeRequirements appends the buildReqs entries whose package does not
// already appear in reqs, also collapsing repeats within buildReqs itself.
func mergeRequirements(reqs, buildReqs []string) []string {
	existing := make(map[string]bool)
	for _, req := range reqs {
		existing[requirementName(req)] = true
	}
	for _, newReq := range buildReqs {
		// Mark as we add so duplicates within buildReqs collapse too.
		if pkg := requirementName(newReq); pkg != "" && !existing[pkg] {
			reqs = append(reqs, newReq)
			existing[pkg] = true
		}
	}
	return reqs
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
		ref, err = findGitRef(ctx, mux, release.Name, version, release, rcfg)
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
			reqs = mergeRequirements(reqs, buildReqs)
		}
	}
	if strings.HasSuffix(a.Filename, ".tar.gz") {
		return &SdistBuild{
			Location: rebuild.Location{
				Repo: rcfg.URI,
				Dir:  dir,
				Ref:  ref,
			},
			PythonVersion: inferPythonVersion(reqs, a.UploadTime),
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
			PythonVersion: inferPythonVersion(reqs, a.UploadTime),
			Requirements:  reqs,
			RegistryTime:  a.UploadTime,
		}, nil
	}
}

var (
	// setuptools 66.1.0 (released 2023-01-20) was the first release whose
	// pkg_resources stopped referencing pkgutil.ImpImporter, which Python 3.12
	// removed. Older setuptools fails to import on 3.12.
	setuptoolsImpImporterFixVersion = "66.1.0"
	setuptoolsImpImporterFixDate    = time.Date(2023, time.January, 20, 0, 0, 0, 0, time.UTC)
	// Upper bounds within a PEP 440 version specifier.
	versionCeilingPat = re.MustCompile(`(<=?|==)\s*([\d.]+)`)
)

// inferPythonVersion pins Python 3.11 when the build may import a setuptools
// older than the ImpImporter fix: any upload predating it, since pip or the
// backend then resolve a contemporary setuptools whatever the backend, and a
// later upload only under a setuptools ceiling below the fix.
func inferPythonVersion(reqs []string, registryTime time.Time) string {
	if registryTime.Before(setuptoolsImpImporterFixDate) {
		return "3.11"
	}
	for _, req := range reqs {
		if !strings.EqualFold(requirementName(req), "setuptools") {
			continue
		}
		for _, m := range versionCeilingPat.FindAllStringSubmatch(req, -1) {
			if c := versionx.ApproxCompare(m[2], setuptoolsImpImporterFixVersion); c < 0 || c == 0 && m[1] == "<" {
				return "3.11"
			}
		}
	}
	return ""
}

var bdistWheelPat = re.MustCompile(`^Generator: bdist_wheel \(([\d\.]+)\)`)
var setuptoolsPat = re.MustCompile(`^Generator: setuptools \(([\d\.]+)\)`)
var flitPat = re.MustCompile(`^Generator: flit ([\d\.]+)`)
var hatchlingPat = re.MustCompile(`^Generator: hatchling ([\d\.]+)`)

// poetry-core is a subset of poetry. We can treat them as different builders.
var poetryPat = re.MustCompile(`^Generator: poetry ([\d\.]+)`)
var poetryCorePat = re.MustCompile(`^Generator: poetry-core ([\d\.]+)`)

// getGenerator returns the pins identifying the wheel's build backend from its
// Generator line. bdist_wheel names the wheel packaging tool, not setuptools,
// whose version is instead bounded by the metadata era.
func getGenerator(wheel, metadata []byte) (reqs []string, err error) {
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
				return []string{"wheel==" + string(matches[1]), setuptoolsCeiling(metadata)}, nil
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

// setuptoolsCeiling bounds a bdist_wheel build's setuptools by its metadata era.
// NOTE: These version ceilings pin with <= rather than == as timewarp will
// fail to resolve equality constraints where registry_time is configured
// before that version's release (which has been observed).
func setuptoolsCeiling(metadata []byte) string {
	switch {
	case !bytes.Contains(metadata, []byte("License-File")):
		// License-File was introduced in later versions, bounding this above.
		return "setuptools<=56.2.0"
	case bytes.Contains(metadata, []byte("Platform: UNKNOWN")):
		// Later versions omit the unknown platform, so this is an older setuptools.
		return "setuptools<=57.5.0"
	default:
		return "setuptools<=67.7.2"
	}
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
