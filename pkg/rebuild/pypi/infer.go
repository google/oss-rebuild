// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"archive/zip"
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"io/fs"
	"log"
	"path"
	re "regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/internal/versionx"
	pypiresolver "github.com/google/oss-rebuild/pkg/rebuild/pypi/parsing"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
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
	"code",
}

var distInfoFieldPat = re.MustCompile(`[-_.]+`)

// InferRepo reads the repository from the links the release was published
// with, falling back to the project's current links, which follow its newest
// release. A link named as a source at either level beats a repository scraped
// from a description. The first hit wins:
//
//	source links, release then project:
//	  - the home page, on a known repo host
//	  - a link named in commonRepoLinks, on a known repo host
//	  - a link named in commonRepoLinks, on any host
//	other mentions, release then project:
//	  - the first repository cited in the description
//	  - a link under any other name, on a known repo host, sponsors excluded
func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	var infos []pypireg.Info
	if release, err := mux.PyPI.Release(ctx, t.Package, t.Version); err != nil {
		log.Printf("Release metadata unavailable [pkg=%s,ver=%s]: %v", t.Package, t.Version, err)
	} else {
		infos = append(infos, release.Info)
	}
	project, err := mux.PyPI.Project(ctx, t.Package)
	if err != nil {
		return "", errors.Wrap(err, "fetching pypi metadata")
	}
	infos = append(infos, project.Info)
	for _, pick := range []func(pypireg.Info) string{repoFromSourceLinks, repoFromOtherLinks} {
		for _, info := range infos {
			if repo := pick(info); repo != "" {
				return uri.CanonicalizeRepoURI(repo)
			}
		}
	}
	return "", errors.New("no git repo")
}

// repoFromSourceLinks picks the repository out of the links named as a source:
// the home page or a source link on a known repo host, else any source link.
func repoFromSourceLinks(info pypireg.Info) string {
	byName := make(map[string]string, len(info.ProjectURLs))
	for name, url := range info.ProjectURLs {
		byName[strings.ReplaceAll(strings.ToLower(name), " ", "")] = url
	}
	var sources []string
	for _, name := range commonRepoLinks {
		if url, ok := byName[name]; ok {
			sources = append(sources, url)
		}
	}
	for _, url := range append([]string{info.Homepage, byName["homepage"]}, sources...) {
		if repo := uri.FindCommonRepo(url); repo != "" {
			return repo
		}
	}
	if len(sources) != 0 {
		return sources[0]
	}
	return ""
}

// repoFromOtherLinks picks the first known repo host cited in the description,
// else one linked under any other name.
func repoFromOtherLinks(info pypireg.Info) string {
	r := uri.FindCommonRepo(info.Description)
	// TODO: Maybe revisit this sponsors logic?
	if r != "" && !strings.Contains(r, "sponsors") {
		return r
	}
	for _, url := range info.ProjectURLs {
		if strings.Contains(url, "sponsors") {
			continue
		}
		if repo := uri.FindCommonRepo(url); repo != "" {
			return repo
		}
	}
	return ""
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

// FindPlatformWheel returns the first platform-specific wheel artifact from the given version's releases.
func FindPlatformWheel(artifacts []pypireg.Artifact) (*pypireg.Artifact, error) {
	for _, r := range artifacts {
		if strings.HasSuffix(r.Filename, ".whl") && !strings.HasSuffix(r.Filename, "none-any.whl") {
			if _, err := platform.ParsePlatformTags(extractPlatformTag(r.Filename)); err == nil {
				return &r, nil
			}
		}
	}
	return nil, fs.ErrNotExist
}

// WheelTags contains parsed PEP 427 wheel tags.
type WheelTags struct {
	Python   string
	ABI      string
	Platform string
}

// extractWheelTags extracts python tag, abi tag, and platform tag from a wheel filename (PEP 427).
func extractWheelTags(filename string) WheelTags {
	if !strings.HasSuffix(filename, ".whl") {
		return WheelTags{}
	}
	stem := strings.TrimSuffix(filename, ".whl")
	parts := strings.Split(stem, "-")
	if len(parts) >= 5 {
		return WheelTags{
			Python:   parts[len(parts)-3],
			ABI:      parts[len(parts)-2],
			Platform: parts[len(parts)-1],
		}
	}
	return WheelTags{}
}

// extractPlatformTag extracts the platform tag from a wheel filename.
func extractPlatformTag(filename string) string {
	return extractWheelTags(filename).Platform
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

// normalizeName applies PyPA name normalization so that equivalent spellings
// of a distribution name, like "Flit.Core" and "flit_core", compare equal.
// https://packaging.python.org/en/latest/specifications/name-normalization/
func normalizeName(name string) string {
	return strings.ToLower(distInfoFieldPat.ReplaceAllString(name, "-"))
}

// Wheel dist-info names use escaped distribution/version components:
// https://packaging.python.org/en/latest/specifications/binary-distribution-format/#escaping-and-unicode
// Name comparisons use PyPA name normalization:
// https://packaging.python.org/en/latest/specifications/name-normalization/
func normalizeDistInfoName(name string) string {
	return strings.ReplaceAll(normalizeName(name), "-", "_")
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

// requirementName extracts the normalized distribution name from a dependency
// specifier, dropping extras, version specifiers, direct references and
// markers, so "SetupTools[core]<=67.7.2" yields "setuptools".
// https://packaging.python.org/en/latest/specifications/dependency-specifiers/#grammar
func requirementName(req string) string {
	fields := strings.FieldsFunc(req, func(r rune) bool { return strings.ContainsRune("=<>~!;[(@ \t", r) })
	if len(fields) == 0 {
		return ""
	}
	return normalizeName(fields[0])
}

// urlMarkerSepPat matches the separator before a direct reference's marker.
var urlMarkerSepPat = re.MustCompile(`[ \t]+;`)

// requirementMarker returns the environment marker of a dependency specifier,
// or "" if it has none.
// NOTE: URLs may contain ';', so the grammar requires whitespace before the
// marker of a direct reference. Names, extras and versions cannot contain '@'
// or ';', so an '@' before the first ';' marks a direct reference.
// https://packaging.python.org/en/latest/specifications/dependency-specifiers/#grammar
func requirementMarker(req string) string {
	semi := strings.IndexByte(req, ';')
	if at := strings.IndexByte(req, '@'); at != -1 && (semi == -1 || at < semi) {
		loc := urlMarkerSepPat.FindStringIndex(req[at:])
		if loc == nil {
			return ""
		}
		return strings.TrimSpace(req[at+loc[1]:])
	}
	if semi == -1 {
		return ""
	}
	return strings.TrimSpace(req[semi+1:])
}

// hasRequirement reports whether reqs name any of the packages under any
// specifier. The names must be normalized.
func hasRequirement(reqs []string, names ...string) bool {
	return slices.ContainsFunc(reqs, func(r string) bool { return slices.Contains(names, requirementName(r)) })
}

// implicitRequirements returns implicit build tools required by any of the detected requirements (e.g. meson-python needs "ninja" but packages don't always declare it).
func implicitRequirements(reqs []string) []string {
	if hasRequirement(reqs, "meson-python", "scikit-build-core") {
		return []string{"ninja"}
	}
	return nil
}

// mergeRequirements appends the buildReqs entries whose package does not
// already appear in reqs, also collapsing repeats within buildReqs itself.
// Entries of one package with different environment markers are all kept, as
// each takes effect in a different environment and the installer evaluates
// them against the build interpreter.
// https://packaging.python.org/en/latest/specifications/dependency-specifiers/#environment-markers
func mergeRequirements(reqs, buildReqs []string) []string {
	pinned := make(map[string]bool)
	for _, req := range reqs {
		pinned[requirementName(req)] = true
	}
	seen := make(map[string]bool)
	for _, newReq := range buildReqs {
		pkg := requirementName(newReq)
		if pkg == "" || pinned[pkg] {
			continue
		}
		if key := pkg + ";" + requirementMarker(newReq); !seen[key] {
			reqs = append(reqs, newReq)
			seen[key] = true
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
	var a *pypireg.Artifact
	for _, art := range release.Artifacts {
		if art.Filename == t.Artifact {
			a = &art
			break
		}
	}
	if a == nil {
		return cfg, errors.Errorf("artifact %s not found in release", t.Artifact)
	}
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
		ref, err = findGitRef(ctx, mux, release.Name, version, release, rcfg)
		if err != nil {
			return cfg, err
		}
		dir = rcfg.Dir
	}
	var submodules []rebuild.Submodule
	if lh != nil {
		submodules = lh.Submodules
	}
	if commit, err := rcfg.Repository.CommitObject(plumbing.NewHash(ref)); err == nil {
		if tree, err := commit.Tree(); err == nil {
			submodules = rebuild.MergeSubmodules(submodules, rebuild.Submodules(tree, rcfg.URI))
		}
	}
	loc := rebuild.Location{Repo: rcfg.URI, Dir: dir, Ref: ref, Submodules: submodules}
	s, err := inferBuild(ctx, t, mux, rcfg, release, a, loc)
	if err != nil {
		return nil, &rebuild.InferenceError{Detail: rebuild.InferenceErrorDetail{Location: loc, Published: a.UploadTime}, Err: err}
	}
	return s, nil
}

// inferBuild chooses the wheel or sdist build for a resolved location.
func inferBuild(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, rcfg *rebuild.RepoConfig, release *pypireg.Release, a *pypireg.Artifact, loc rebuild.Location) (rebuild.Strategy, error) {
	name, version := t.Package, t.Version
	ref, dir := loc.Ref, loc.Dir
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
	var zr *zip.Reader
	if strings.HasSuffix(a.Filename, ".whl") {
		zr, err = zip.NewReader(bytes.NewReader(body), a.Size)
		if err != nil {
			return nil, errors.Wrapf(err, "[INTERNAL] Failed to initialize upstream zip reader")
		}
		reqs, err = inferRequirements(release.Name, version, zr)
		if err != nil {
			return nil, err
		}
	} else if strings.HasSuffix(a.Filename, ".tar.gz") {
		// For .tar.gz files (source distributions), we don't infer requirements from the archive
		// We'll get them from pyproject.toml below
		reqs = []string{}
	}
	submodules := loc.Submodules
	commit, err := rcfg.Repository.CommitObject(plumbing.NewHash(ref))
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to get commit object")
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to get tree")
	}
	// Extract pyproject.toml requirements.
	{
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
		reqs = mergeRequirements(reqs, implicitRequirements(reqs))
	}
	if strings.HasSuffix(a.Filename, ".tar.gz") {
		return &SdistBuild{
			Location: rebuild.Location{
				Repo:       rcfg.URI,
				Dir:        dir,
				Ref:        ref,
				Submodules: submodules,
			},
			PythonVersion: inferPythonVersion(reqs, a.UploadTime),
			Requirements:  reqs,
			RegistryTime:  a.UploadTime,
		}, nil
	} else if strings.HasSuffix(a.Filename, ".whl") && !strings.HasSuffix(a.Filename, "none-any.whl") {
		tags := extractWheelTags(a.Filename)
		parsedTags, err := platform.ParsePlatformTags(tags.Platform)
		if err != nil {
			return nil, errors.Wrapf(err, "unsupported platform tag in wheel filename %s", a.Filename)
		}
		highest, err := platform.HighestLibcVersionTag(parsedTags)
		if err != nil {
			return nil, errors.Wrapf(err, "determining target libc for wheel %s", a.Filename)
		}
		sysDeps, err := inferELFSystemDeps(zr, highest.LibcImpl)
		if err != nil {
			log.Println(errors.Wrap(err, "extracting ELF system deps from wheel"))
		}
		cibwSpecs, err := extractCibuildwheelSpecs(tree)
		if err != nil {
			log.Println(errors.Wrap(err, "Failed to extract cibuildwheel specs"))
		}
		buildEnv, envReqs, err := inferWheelBuildEnv(tree, dir, release.Name, zr, tags, reqs)
		if err != nil {
			return nil, errors.Wrap(err, "inferring wheel build env")
		}
		reqs = mergeRequirements(reqs, envReqs)
		return &PlatformWheelBuild{
			Location: rebuild.Location{
				Repo:       rcfg.URI,
				Dir:        dir,
				Ref:        ref,
				Submodules: submodules,
			},
			PythonTag:    tags.Python,
			ABITag:       tags.ABI,
			PlatformTag:  tags.Platform,
			Requirements: reqs,
			BuildEnv:     buildEnv,
			SystemDeps:   sysDeps,
			RegistryTime: a.UploadTime,
			BaseImage:    inferBaseImage(platform.CibuildwheelPins, tags.Platform, a.UploadTime, cibwSpecs...),
		}, nil
	} else {
		return &PureWheelBuild{
			Location: rebuild.Location{
				Repo:       rcfg.URI,
				Dir:        dir,
				Ref:        ref,
				Submodules: submodules,
			},
			PythonVersion: inferPythonVersion(reqs, a.UploadTime),
			Requirements:  reqs,
			RegistryTime:  a.UploadTime,
			PythonTag:     pythonTag(a.Filename, reqs),
		}, nil
	}
}

// inferBaseImage returns the build image that cibuildwheel pinned for
// platformTag, according to pins. Any cibuildwheel version specs from the CI
// config take precedence over the version that was current when the wheel was
// uploaded. If no pin is known it returns an empty string, selecting the
// unpinned image at build time.
func inferBaseImage(pins platform.CibuildwheelPinTable, platformTag string, uploaded time.Time, cibwSpecs ...string) string {
	repo, err := platform.SelectBaseImage(platformTag)
	if err != nil {
		log.Println(errors.Wrap(err, "Failed to select base image"))
		return ""
	}
	if len(cibwSpecs) > 0 {
		if ref, version, ok := pins.ImageAt(repo, uploaded, cibwSpecs...); ok {
			log.Printf("Using base image %s pinned by cibuildwheel %s from the CI config", ref, version)
			return ref
		}
		log.Printf("No pin known for %s in cibuildwheel %v from the CI config, falling back to the upload time", repo, cibwSpecs)
	}
	if generated := platform.CibuildwheelPinsGeneratedAt; uploaded.After(generated) {
		log.Printf("The cibuildwheel pin table generated at %s predates the upload, so releases published since are missing from it. Regenerate it with go generate ./pkg/rebuild/pypi/platform", generated.Format(time.RFC3339))
	}
	// TODO: The upload time stands in for the build time, which PyPI does not
	// record. A wheel built well before its upload, such as one that waited
	// for a release approval, can get the image of a newer cibuildwheel
	// release. Fingerprints in the wheel could verify the pick, such as the
	// compiler in .comment, the CPython version in PY_VERSION_HEX and the
	// auditwheel version in its SBOM.
	ref, version, ok := pins.ImageAt(repo, uploaded)
	if !ok {
		log.Printf("No cibuildwheel pin known for %s at %s, using the unpinned image", repo, uploaded.Format(time.RFC3339))
		return ""
	}
	log.Printf("Using base image %s pinned by cibuildwheel %s", ref, version)
	return ref
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
		if requirementName(req) != "setuptools" {
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

// py2WheelPat captures a wheel's python tag when it names a non-py3 language.
var py2WheelPat = re.MustCompile(`-(py2(?:\.py3)?)-[^-]+-[^-]+\.whl$`)

// pythonTag returns the tag setuptools has to be told to give the wheel, if any.
func pythonTag(filename string, reqs []string) string {
	if m := py2WheelPat.FindStringSubmatch(filename); m != nil && hasRequirement(reqs, "setuptools", "wheel") {
		return m[1]
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
var pdmBackendPat = re.MustCompile(`^Generator: pdm-backend \(([\d\.]+)\)`)
var uvBuildPat = re.MustCompile(`^Generator: uv ([\d\.]+)`)
var mesonPat = re.MustCompile(`^Generator: meson(?: \(?([\d\.]+)\)?)?\s*$`)
var scikitBuildCorePat = re.MustCompile(`^Generator: scikit-build-core \(?([\d\.]+)\)?`)

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
			} else if matches := pdmBackendPat.FindSubmatch(line); matches != nil {
				return []string{"pdm-backend==" + string(matches[1])}, nil
			} else if matches := uvBuildPat.FindSubmatch(line); matches != nil {
				return []string{"uv-build==" + string(matches[1])}, nil
			} else if matches := mesonPat.FindSubmatch(line); matches != nil {
				if ver := string(matches[1]); ver != "" {
					return []string{"meson-python==" + ver}, nil
				}
				return []string{}, nil
			} else if matches := scikitBuildCorePat.FindSubmatch(line); matches != nil {
				return []string{"scikit-build-core==" + string(matches[1])}, nil
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

// versionClause matches one PEP 440 version clause like ">= 3.0" or "==3.2.*".
const versionClause = `(?:~=|===?|!=|<=|>=|<|>)\s*[0-9A-Za-z_.*+!-]+`

var (
	// setupEnvGatePat matches setup.py env vars that opt into native compilation.
	setupEnvGatePat = re.MustCompile(`os\.(?:getenv|environ\.get)\(\s*["']([A-Z_][A-Z0-9_]*_(?:USE_MYPYC|USE_CYTHON|WITH_CYTHON|CYTHON_ABI3))["']`)
	// setupOptionalCibuildwheelPat matches a setup.py extension that is optional
	// unless CIBUILDWHEEL is set, as for dulwich's Rust extension.
	setupOptionalCibuildwheelPat = re.MustCompile(`optional\s*=\s*[^\n]*os\.(?:getenv|environ\.get)\(\s*["']CIBUILDWHEEL["']`)
	// setupCythonImportPat matches a setup.py that compiles extensions via Cython.
	setupCythonImportPat = re.MustCompile(`(?m)^\s*(?:from\s+Cython\b|import\s+[Cc]ython\b)`)
	// cythonSpecPat matches a quoted Cython requirement like "Cython>=3.0, <3.1".
	cythonSpecPat = re.MustCompile(`["']\s*((?i:cython)\s*` + versionClause + `(?:\s*,\s*` + versionClause + `)*)\s*[;"']`)
	// cythonABIModulePat matches the Cython version embedded in extension modules.
	// NOTE: CYTHON_ABI is the Cython version with '.' replaced by '_', optionally
	// followed by a PEP 440 pre-release tag (a1, b2, rc1) and, from 3.1 on, by
	// lowercase feature suffixes such as "limited" or "noamsend".
	cythonABIModulePat = re.MustCompile(`_cython_(\d+)_(\d+)_(\d+)((?:a|b|rc)\d+)?(?:[a-z]|\b)`)
	// requirementsCommentPat matches a pip requirements file comment.
	requirementsCommentPat = re.MustCompile(`(?:^|\s)#.*`)
	// requirementsIncludePat matches a pip requirements file include line.
	requirementsIncludePat = re.MustCompile(`^(?:-r\s*|--requirement(?:\s+|=))(\S+)$`)
)

// inferWheelBuildEnv inspects the upstream wheel and repository build files
// for environment variable gates and companion build requirements needed to
// compile mypyc, Cython, or optional Rust extension modules.
func inferWheelBuildEnv(tree *object.Tree, dir, pkg string, zr *zip.Reader, tags WheelTags, reqs []string) (map[string]string, []string, error) {
	if tree == nil || zr == nil {
		return nil, nil, nil
	}
	// NOTE: inferRequirements derives setuptools from the wheel's Generator header
	// even when pyproject.toml selects flit_core, which ignores setup.py.
	if !hasRequirement(reqs, "setuptools") || hasRequirement(reqs, "flit-core") {
		return nil, nil, nil
	}
	hasSO, hasMypycSO, hasABI3SO, cythonVersion := inspectWheelNativeModules(zr)
	if !hasSO {
		return nil, nil, nil
	}
	setupFile, err := tree.File(path.Join(dir, "setup.py"))
	if errors.Is(err, object.ErrFileNotFound) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, errors.Wrap(err, "finding setup.py")
	}
	setupSrc, err := setupFile.Contents()
	if err != nil {
		return nil, nil, errors.Wrap(err, "reading setup.py")
	}
	env := make(map[string]string)
	var extraReqs []string
	usedMypyc, usedCython := false, false
	for _, m := range setupEnvGatePat.FindAllStringSubmatch(setupSrc, -1) {
		key := m[1]
		switch {
		case strings.HasSuffix(key, "_USE_MYPYC"):
			if !hasMypycSO {
				continue
			}
			usedMypyc = true
		case strings.HasSuffix(key, "_USE_CYTHON"), strings.HasSuffix(key, "_WITH_CYTHON"):
			usedCython = true
		case strings.HasSuffix(key, "_CYTHON_ABI3"):
			if tags.ABI != "abi3" && !hasABI3SO {
				continue
			}
		}
		env[key] = "1"
	}
	if setupOptionalCibuildwheelPat.MatchString(setupSrc) {
		env["CIBUILDWHEEL"] = "1"
	}
	if usedMypyc {
		if _, err := tree.File(path.Join(dir, "mypyc/build.py")); errors.Is(err, object.ErrFileNotFound) {
			if !hasRequirement(reqs, "mypy") {
				extraReqs = append(extraReqs, "mypy")
			}
		} else if err != nil {
			return nil, nil, errors.Wrap(err, "finding mypyc/build.py")
		} else {
			buildReqs, err := readRequirementsFile(tree, dir, "build-requirements.txt", nil)
			if err != nil {
				return nil, nil, err
			}
			extraReqs = append(extraReqs, buildReqs...)
		}
	}
	// NOTE: Cython's own setup.py imports Cython from the source checkout and
	// must not add a circular build dependency on itself.
	if (usedCython || setupCythonImportPat.MatchString(setupSrc)) && !hasRequirement(reqs, "cython") && normalizeName(pkg) != "cython" {
		cythonReq, err := findCythonRequirement(tree, dir, setupSrc)
		if err != nil {
			return nil, nil, err
		}
		extraReqs = append(extraReqs, pinCythonRequirement(cythonReq, cythonVersion))
	}
	if len(env) == 0 {
		return nil, extraReqs, nil
	}
	return env, extraReqs, nil
}

func inspectWheelNativeModules(zr *zip.Reader) (hasSO, hasMypycSO, hasABI3SO bool, cythonVersion string) {
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || isBundledLibPath(f.Name) {
			continue
		}
		if !strings.HasSuffix(f.Name, ".so") && !strings.Contains(f.Name, ".so.") {
			continue
		}
		hasSO = true
		if strings.Contains(f.Name, "__mypyc") {
			hasMypycSO = true
		}
		if strings.HasSuffix(f.Name, ".abi3.so") {
			hasABI3SO = true
		}
		if cythonVersion == "" && f.UncompressedSize64 <= maxELFFileSize {
			cythonVersion = extractELFFileCythonVersion(f)
		}
	}
	return hasSO, hasMypycSO, hasABI3SO, cythonVersion
}

func extractELFFileCythonVersion(f *zip.File) string {
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return ""
	}
	ef, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	for _, secName := range []string{".rodata", ".dynstr"} {
		sec := ef.Section(secName)
		if sec == nil {
			continue
		}
		secData, err := sec.Data()
		if err != nil {
			continue
		}
		if m := cythonABIModulePat.FindSubmatch(secData); m != nil {
			return fmt.Sprintf("%s.%s.%s%s", m[1], m[2], m[3], m[4])
		}
	}
	return ""
}

// pinCythonRequirement replaces req's version constraint with cythonVersion
// unless req already pins an exact version. Wildcards like "==3.2.*" are not
// exact, and an "==" inside the marker does not constrain the version.
func pinCythonRequirement(req, cythonVersion string) string {
	spec, _, _ := strings.Cut(req, ";")
	if cythonVersion == "" || requirementName(req) != "cython" || (strings.Contains(spec, "==") && !strings.Contains(spec, "*")) {
		return req
	}
	if marker := requirementMarker(req); marker != "" {
		return "Cython==" + cythonVersion + "; " + marker
	}
	return "Cython==" + cythonVersion
}

// findCythonRequirement returns a version-constrained Cython requirement from
// setup.py or from the in-tree PEP 517 backend module named by [build-system]
// build-backend, defaulting to "Cython" when neither declares one.
func findCythonRequirement(tree *object.Tree, dir, setupSrc string) (string, error) {
	if m := cythonSpecPat.FindStringSubmatch(setupSrc); m != nil {
		return m[1], nil
	}
	pyproject, err := tree.File(path.Join(dir, "pyproject.toml"))
	if errors.Is(err, object.ErrFileNotFound) {
		return "Cython", nil
	} else if err != nil {
		return "", errors.Wrap(err, "finding pyproject.toml")
	}
	modulePaths, err := pypiresolver.InTreeBackendModulePaths(pyproject)
	// NOTE: A malformed pyproject.toml only rules out the in-tree backend.
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		return "Cython", nil
	} else if err != nil {
		return "", errors.Wrap(err, "locating in-tree build backend")
	}
	for _, modulePath := range modulePaths {
		candidate := path.Join(dir, modulePath)
		f, err := tree.File(candidate)
		if errors.Is(err, object.ErrFileNotFound) {
			continue
		} else if err != nil {
			return "", errors.Wrapf(err, "finding %s", candidate)
		}
		src, err := f.Contents()
		if err != nil {
			return "", errors.Wrapf(err, "reading %s", candidate)
		}
		if m := cythonSpecPat.FindStringSubmatch(src); m != nil {
			return m[1], nil
		}
	}
	return "Cython", nil
}

func readRequirementsFile(tree *object.Tree, dir, relPath string, visited map[string]bool) ([]string, error) {
	if visited == nil {
		visited = make(map[string]bool)
	}
	cleanPath := path.Clean(path.Join(dir, relPath))
	if visited[cleanPath] {
		return nil, nil
	}
	visited[cleanPath] = true
	f, err := tree.File(cleanPath)
	if errors.Is(err, object.ErrFileNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, errors.Wrapf(err, "finding %s", cleanPath)
	}
	content, err := f.Contents()
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", cleanPath)
	}
	var reqs []string
	baseDir := rebuild.DirOf(cleanPath)
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(requirementsCommentPat.ReplaceAllString(rawLine, ""))
		if line == "" {
			continue
		}
		if m := requirementsIncludePat.FindStringSubmatch(line); m != nil {
			inc, err := readRequirementsFile(tree, baseDir, m[1], visited)
			if err != nil {
				return nil, err
			}
			reqs = append(reqs, inc...)
			continue
		}
		if strings.HasPrefix(line, "-") {
			continue
		}
		// NOTE: Continuations and per-requirement options like --hash are not
		// parsed, and passing them through would yield invalid requirements.
		if strings.HasSuffix(line, `\`) || strings.Contains(line, " --") || strings.Contains(line, "\t--") {
			return nil, errors.Errorf("unsupported syntax in %s: %q", cleanPath, line)
		}
		reqs = append(reqs, line)
	}
	return reqs, nil
}
