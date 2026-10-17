// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
	"github.com/google/oss-rebuild/pkg/vcs/gitscan"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

const (
	mavenBuildTool     = "maven"
	gradleBuildTool    = "gradle"
	sbtBuildTool       = "sbt"
	antBuildTool       = "ant"
	ivyBuildTool       = "ivy"
	leiningenBuildTool = "leiningen"
	npmBuildTool       = "npm"
	millBuildTool      = "mill"
)

const fallbackJDK = "11"

func (Rebuilder) InferRepo(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux) (string, error) {
	pom, err := NewPomXML(ctx, t, mux)
	if err != nil {
		return "", err
	}
	// The scm URL is often a web viewer or retired host, so the connections
	// are candidates too. A parent's scm stands in only when the POM has none,
	// since org parents such as org.apache:apache name their own repositories.
	scm := pom
	for !scm.hasSCM() && scm.Parent.ArtifactID != "" {
		scm, err = ResolveParentPom(ctx, scm, mux)
		if err != nil {
			return "", errors.Errorf("failed to resolve parent POM for %s: %v", t.Package, err)
		}
	}
	candidates := []string{scm.SCMURL, scm.SCMConnection, scm.SCMDeveloperConnection, scm.Repo()}
	for i, c := range candidates {
		candidates[i] = pom.Interpolate(c)
	}
	for _, c := range candidates {
		if uri.FindCommonRepo(c) != "" {
			return uri.CanonicalizeRepoURI(c)
		}
	}
	for _, c := range candidates {
		if repo := apacheMirror(ctx, c); repo != "" {
			return repo, nil
		}
	}
	return uri.CanonicalizeRepoURI(candidates[len(candidates)-1])
}

var (
	apacheGitRE = regexp.MustCompile(`(?i)(?:gitbox|git-wip-us|git)\.apache\.org/repos/asf(?:\?p=|/)([\w.-]+?)(?:\.git)?(?:[;/?#]|$)`)
	apacheSVNRE = regexp.MustCompile(`(?i)svn\.apache\.org/(?:repos/asf|viewvc)/([^?#]+)`)
)

// apacheMirror maps an Apache git or Subversion URL to the project's GitHub
// mirror, checking that the mirror exists since Subversion paths only
// suggest its name: commons/proper/logging/trunk is apache/commons-logging.
func apacheMirror(ctx context.Context, u string) string {
	var names []string
	if m := apacheGitRE.FindStringSubmatch(u); m != nil {
		names = []string{m[1]}
	} else if m := apacheSVNRE.FindStringSubmatch(u); m != nil && !strings.Contains(m[1], "${") {
		var segs []string
		for _, s := range strings.Split(m[1], "/") {
			if s == "trunk" || s == "tags" || s == "branches" {
				break
			}
			if s != "" && s != "proper" && s != "jakarta" {
				segs = append(segs, s)
			}
		}
		if len(segs) > 0 {
			names = slices.Compact([]string{strings.Join(segs, "-"), segs[len(segs)-1]})
		}
	}
	for _, name := range names {
		repo := "https://github.com/apache/" + strings.ToLower(name)
		req, err := http.NewRequest(http.MethodHead, repo, nil)
		if err != nil {
			continue
		}
		resp, err := rebuild.DoContext(ctx, req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
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

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repoConfig *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	head, err := repoConfig.Repository.Head()
	if err != nil {
		return nil, errors.Wrapf(err, "resolving HEAD [repo=%s]", repoConfig.URI)
	}
	commitObject, err := repoConfig.Repository.CommitObject(head.Hash())
	if err != nil {
		return nil, errors.Wrapf(err, "resolving HEAD commit [repo=%s]", repoConfig.URI)
	}
	// TODO: It is possible that the build tool would have changed between the HEAD commit and the commit used for the build.
	// Although unlikely, we should ideally check out the specific commit used for the build if known.
	// This would require to do version heuristic first to determine the correct commit/tag.
	buildTool, err := inferBuildTool(commitObject)
	if err != nil {
		return nil, errors.Wrapf(err, "inferring build tool")
	}
	switch buildTool {
	case mavenBuildTool:
		mavenStrategy, err := MavenInfer(ctx, t, mux, repoConfig)
		if err != nil {
			return nil, errors.Wrapf(err, "inferring Maven strategy")
		}
		return mavenStrategy, nil
	case gradleBuildTool:
		gradleStrategy, err := GradleInfer(ctx, t, mux, repoConfig)
		if err != nil {
			return nil, errors.Wrapf(err, "inferring Gradle strategy")
		}
		return gradleStrategy, nil
	case sbtBuildTool, antBuildTool, ivyBuildTool, leiningenBuildTool, npmBuildTool, millBuildTool:
		return nil, errors.Errorf("build tool %s is recognized but not yet supported", buildTool)
	default:
		return nil, errors.New("no recognized build tool")
	}
}

// buildToolRank breaks ties at equal depth: a JVM build file outranks a
// package.json or build.xml beside it, which usually serve tooling rather than
// the release.
var buildToolRank = map[string]int{mavenBuildTool: 0, gradleBuildTool: 1, sbtBuildTool: 2, millBuildTool: 3, leiningenBuildTool: 4, antBuildTool: 5, ivyBuildTool: 6, npmBuildTool: 7}

// inferBuildTool scans the repository for build tool indicators and returns the most probable build tool.
// It checks for the presence of "pom.xml" (Maven) and "gradlew" (Gradle) files, prioritizing the tool found in the shallowest directory.
// The build tool located in the directory closest to the repository root is chosen, as it likely represents the primary build system.
// The deeper the file is in the directory structure, it is more likely to be a red herring (e.g., examples, test resources).
func inferBuildTool(commit *git2.Commit) (string, error) {
	var bestBuildTool string
	minDepth := math.MaxInt

	fileIter, err := commit.Files()
	if err != nil {
		return "", errors.Wrap(err, "listing commit files")
	}
	err = fileIter.ForEach(func(f *git2.File) error {
		currentDepth := strings.Count(f.Name, "/")
		if currentDepth > minDepth {
			// No need to check deeper files if we already have a shallower candidate
			return nil
		}
		var identifiedTool string
		fileName := path.Base(f.Name)
		switch {
		// Check for Maven's build file
		case fileName == "pom.xml":
			// Per Maven conventions, skip non-"pom.xml" files and those inside a `src` directory (unlikely to contain metadata).
			// Reference: https://maven.apache.org/guides/introduction/introduction-to-the-standard-directory-layout.html
			if !strings.HasPrefix(f.Name, "src/") && !strings.Contains(f.Name, "/src/") {
				identifiedTool = mavenBuildTool
			}
		// Check for Gradle wrapper or compatible build files
		// It is common practice to include the Gradle wrapper script (`gradlew`) at the root of the project.
		// Reference: https://docs.gradle.org/current/userguide/gradle_wrapper_basics.html
		case fileName == "gradlew" || strings.HasSuffix(fileName, ".gradle") || strings.HasSuffix(fileName, ".gradle.kts"):
			identifiedTool = gradleBuildTool
		// Simple Build Tool (sbt) is to build Scala projects
		case fileName == "build.sbt":
			identifiedTool = sbtBuildTool
		// Apache Ant build file
		case fileName == "build.xml":
			identifiedTool = antBuildTool
		// Apache Ivy file used in conjunction with Ant
		case fileName == "ivy.xml":
			identifiedTool = ivyBuildTool
		// Leiningen build file for Clojure projects
		case fileName == "project.clj":
			identifiedTool = leiningenBuildTool
		// Build file for Node.js projects
		case fileName == "package.json":
			identifiedTool = npmBuildTool
		// Build file compatible with the Mill build tool
		// Reference: https://mill-build.org/mill/javalib/intro.html
		case fileName == "build.sc" || fileName == "build.mill":
			identifiedTool = millBuildTool
		}
		if identifiedTool != "" && (currentDepth < minDepth || buildToolRank[identifiedTool] < buildToolRank[bestBuildTool]) {
			bestBuildTool = identifiedTool
			minDepth = currentDepth
		}
		return nil
	})
	if err != nil {
		return "", errors.Wrap(err, "iterating commit files")
	}
	if bestBuildTool != "" {
		return bestBuildTool, nil
	}

	return "", errors.Errorf("build tool inference failed")
}

// upstreamJar is what the published jar records about its build.
type upstreamJar struct {
	JDK   string // from the manifest, else the bytecode
	Maven string // from Created-By
	CRLF  bool   // META-INF pom.xml has CRLF endings
}

var createdByMavenRe = regexp.MustCompile(`^Apache Maven (\d+\.\d+\.\d+)`)

func readUpstreamJar(ctx context.Context, name, version string, mux rebuild.RegistryMux) (upstreamJar, error) {
	var j upstreamJar
	releaseFile, err := mux.Maven.ReleaseFile(ctx, name, version, maven.TypeJar)
	if err != nil {
		return j, errors.Wrap(err, "fetching jar file")
	}
	defer releaseFile.Close()
	jarBytes, err := io.ReadAll(releaseFile)
	if err != nil {
		return j, errors.Wrap(err, "reading jar file")
	}
	zr, err := zip.NewReader(bytes.NewReader(jarBytes), int64(len(jarBytes)))
	if err != nil {
		return j, errors.Wrap(err, "unzipping jar file")
	}
	attrs := manifestAttrs(zr)
	switch {
	case attrs["Build-Jdk"] != "":
		j.JDK = attrs["Build-Jdk"]
	case attrs["Build-Jdk-Spec"] != "":
		j.JDK = attrs["Build-Jdk-Spec"]
	case attrs["Created-By"] != "" && attrs["Created-By"][0] >= '0' && attrs["Created-By"][0] <= '9':
		// The jar tool writes its own version, e.g. "11.0.4 (Oracle Corporation)".
		j.JDK, _, _ = strings.Cut(attrs["Created-By"], " ")
	}
	if m := createdByMavenRe.FindStringSubmatch(attrs["Created-By"]); m != nil {
		j.Maven = m[1]
	}
	if j.JDK == "" {
		if j.JDK, err = inferJDKFromBytecode(zr); err != nil {
			return j, errors.Wrap(err, "inferring JDK from bytecode")
		}
	}
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "META-INF/maven/") || path.Base(f.Name) != "pom.xml" {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return j, errors.Wrapf(err, "opening %s", f.Name)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return j, errors.Wrapf(err, "reading %s", f.Name)
		}
		j.CRLF = bytes.Contains(b, []byte("\r\n"))
		break
	}
	return j, nil
}

func manifestAttrs(zr *zip.Reader) map[string]string {
	attrs := map[string]string{}
	f, err := zr.Open("META-INF/MANIFEST.MF")
	if err != nil {
		return attrs
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return attrs
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			break // end of the main section
		}
		if k, v, ok := strings.Cut(line, ":"); ok && !strings.HasPrefix(line, " ") {
			attrs[k] = strings.TrimSpace(v)
		}
	}
	return attrs
}

// inferOrFallbackToDefaultJDK picks the downloadable JDK closest to the one
// that built the published jar.
func inferOrFallbackToDefaultJDK(ctx context.Context, name, version string, mux rebuild.RegistryMux) (string, error) {
	j, err := readUpstreamJar(ctx, name, version, mux)
	if err != nil {
		return "", errors.Wrap(err, "fetching JDK")
	}
	return jdkFor(j.JDK), nil
}

// jdkFor maps a recorded JDK version to the closest JDKDownloadURLs key: the
// same release if available, else the nearest update of the same major.
// Javac output changes between releases, so the exact build matters.
func jdkFor(raw string) string {
	v := strings.TrimSpace(raw)
	if i := strings.IndexAny(v, "+- "); i >= 0 {
		v = v[:i] // "11.0.4+10", "1.8.0_181-b13"
	}
	if v == "" {
		log.Printf("no JDK version inferred, falling back to JDK %s", fallbackJDK)
		return fallbackJDK
	}
	if _, ok := JDKDownloadURLs[v]; ok {
		return v
	}
	major, update := splitJDK(v)
	if n, err := strconv.Atoi(major); err == nil && n < 8 {
		// JDK 8 is the oldest available and still compiles 1.5 through 1.7 sources.
		major, update = "8", -1
	}
	best, bestDist := "", math.MaxInt
	if update >= 0 {
		for k := range JDKDownloadURLs {
			km, ku := splitJDK(k)
			if km != major || ku < 0 {
				continue
			}
			d := ku - update
			if d < 0 {
				d = -d
			}
			// Ties go to the later update, making the choice independent of map order.
			if d < bestDist || (d == bestDist && ku > update) {
				best, bestDist = k, d
			}
		}
	}
	if best != "" {
		log.Printf("Using JDK %s for recorded version %q", best, raw)
		return best
	}
	if _, ok := JDKDownloadURLs[major]; ok {
		log.Printf("Using JDK %s for recorded version %q", major, raw)
		return major
	}
	log.Printf("Could not find a downloadable JDK for %q, falling back to JDK %s", raw, fallbackJDK)
	return fallbackJDK
}

// splitJDK splits a JDK version into its major release and update number,
// the update being -1 when absent: "1.8.0_181" and "8u181" are ("8", 181),
// "11.0.4" is ("11", 4) and "17" is ("17", -1).
func splitJDK(v string) (major string, update int) {
	update = -1
	if rest, ok := strings.CutPrefix(v, "1."); ok {
		m, tail, _ := strings.Cut(rest, ".")
		if _, u, ok := strings.Cut(tail, "_"); ok {
			if n, err := strconv.Atoi(u); err == nil {
				update = n
			}
		}
		return m, update
	}
	if m, u, ok := strings.Cut(v, "u"); ok {
		if n, err := strconv.Atoi(u); err == nil {
			return m, n
		}
	}
	parts := strings.Split(v, ".")
	if len(parts) == 3 && parts[1] == "0" {
		if n, err := strconv.Atoi(parts[2]); err == nil {
			update = n
		}
	}
	return parts[0], update
}

// inferJDKFromBytecode identifies the lowest JDK version that can run the provided JAR's bytecode.
func inferJDKFromBytecode(jarZip *zip.Reader) (string, error) {
	for _, file := range jarZip.File {
		if strings.HasSuffix(file.Name, ".class") {
			classFile, err := file.Open()
			if err != nil {
				continue
			}
			defer classFile.Close()
			classBytes, err := io.ReadAll(classFile)
			if err != nil {
				continue
			}
			majorVersion, err := getClassFileMajorVersion(classBytes)
			if err != nil {
				return "", errors.Wrap(err, "parsing class file for major version")
			}
			return fmt.Sprintf("%d", majorVersion), nil
		}
	}
	return "", errors.New("no .class files found in jar")
}

// getClassFileMajorVersion extracts the major version from Java class file bytes
func getClassFileMajorVersion(classBytes []byte) (int, error) {
	if len(classBytes) < 8 {
		return 0, errors.New("class file too short")
	}
	// Check magic number (0xCAFEBABE)
	if classBytes[0] != 0xCA || classBytes[1] != 0xFE || classBytes[2] != 0xBA || classBytes[3] != 0xBE {
		return 0, errors.New("invalid class file magic number")
	}
	// Skip minor version (bytes 4-5) as it is always 0 since Java 1.1 and read major version (bytes 6-7)
	// JDK and classfile versions: https://javaalmanac.io/bytecode/versions/
	// Position of bytes for version in classfile: https://docs.oracle.com/javase/specs/jvms/se21/html/jvms-4.html
	bytecodeToVersionOffset := uint16(44)
	majorVersion := (uint16(classBytes[6]) << 8) | uint16(classBytes[7]) - bytecodeToVersionOffset
	return int(majorVersion), nil
}

// findClosestCommitToSource attempts to find the git commit that best matches the contents of a source JAR.
func findClosestCommitToSource(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repo *git2.Repository) (*git2.Commit, error) {
	sourceJar, err := mux.Maven.ReleaseFile(ctx, t.Package, t.Version, maven.TypeSources)
	if err != nil {
		return nil, err
	}
	defer sourceJar.Close()
	zipData, err := io.ReadAll(sourceJar)
	if err != nil {
		return nil, err
	}
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, err
	}
	hashes, err := gitscan.BlobHashesFromZip(zipReader)
	if err != nil {
		return nil, errors.Wrap(err, "hashing source jar contents")
	}
	blobs := make([]git2.Hash, len(hashes))
	for i, h := range hashes {
		blobs[i] = git2.Hash(h)
	}
	searchStrategy := gitscan.ReachableTreeCount{}
	closest, matched, total, err := searchStrategy.Search(ctx, repo, blobs)
	if err != nil {
		return nil, errors.Wrap(err, "searching for matching commit based on source jar")
	}
	log.Printf("commits (%d): %v", len(closest), closest)
	log.Printf("matched %d/%d files using git index scan", matched, total)
	if len(closest) == 0 {
		// No matches found so we will not use the source jar heuristic, but this is not an error.
		// The caller should try other heuristics.
		log.Printf("no matching commit found using source jar heuristic")
		return nil, nil
	}
	// TODO: use a better heuristic here like using commit time
	commitString := closest[0]
	// Verify if commit exists in the repository
	commitHash := git2.NewHash(commitString)
	commitObject, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, errors.Wrapf(err, "resolving commit %s", commitString)
	}
	return commitObject, nil
}
