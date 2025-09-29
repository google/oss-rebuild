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
	"path"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
	"github.com/google/oss-rebuild/pkg/vcs/gitscan"
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
	for pom.SCMURL == "" && pom.Parent.ArtifactID != "" {
		pom, err = ResolveParentPom(ctx, pom, mux)
		if err != nil {
			return "", errors.Errorf("failed to resolve parent POM for %s: %v", t.Package, err)
		}
	}
	return uri.CanonicalizeRepoURI(pom.Repo())
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

func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repoConfig *rebuild.RepoConfig, hint rebuild.Strategy) (rebuild.Strategy, error) {
	head, _ := repoConfig.Repository.Head()
	commitObject, _ := repoConfig.Repository.CommitObject(head.Hash())
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

// inferBuildTool scans the repository for build tool indicators and returns the most probable build tool.
// It checks for the presence of "pom.xml" (Maven) and "gradlew" (Gradle) files, prioritizing the tool found in the shallowest directory.
// The build tool located in the directory closest to the repository root is chosen, as it likely represents the primary build system.
// The deeper the file is in the directory structure, it is more likely to be a red herring (e.g., examples, test resources).
func inferBuildTool(commit *object.Commit) (string, error) {
	var bestBuildTool string
	minDepth := math.MaxInt

	fileIter, _ := commit.Files()
	fileIter.ForEach(func(f *object.File) error {
		currentDepth := strings.Count(f.Name, "/")
		if currentDepth >= minDepth {
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
		if identifiedTool != "" {
			bestBuildTool = identifiedTool
			minDepth = currentDepth
		}
		return nil
	})
	if bestBuildTool != "" {
		return bestBuildTool, nil
	}

	return "", errors.Errorf("build tool inference failed")
}

// inferJDKAndTargetVersion gets the build and target JDK versions from the artifact.
// The target version is always inferred from the Java bytecode.
// The build version is inferred from the MANIFEST.MF file, falling back to the target version,
// and finally to a default JDK if no version can be determined.
func inferJDKAndTargetVersion(ctx context.Context, name, version string, mux rebuild.RegistryMux) (buildJDK string, targetJDK string, err error) {
	releaseFile, err := mux.Maven.ReleaseFile(ctx, name, version, maven.TypeJar)
	if err != nil {
		return "", "", errors.Wrap(err, "fetching jar file")
	}
	defer releaseFile.Close()
	jarBytes, err := io.ReadAll(releaseFile)
	if err != nil {
		return "", "", errors.Wrap(err, "reading jar file")
	}
	zipReader, err := zip.NewReader(bytes.NewReader(jarBytes), int64(len(jarBytes)))
	if err != nil {
		return "", "", errors.Wrap(err, "unzipping jar file")
	}
	targetJDK, err = inferJDKFromBytecode(zipReader)
	if err != nil {
		// We fail if we get corrupt or no bytecode as we won't be able to build them anyway so it is better to fail early.
		// Ideally, we should preempt these cases during inference of build tool.
		return "", "", errors.Wrap(err, "inferring target JDK from bytecode")
	}
	buildJDK, err = inferJDKFromManifest(zipReader)
	if err != nil {
		return "", "", errors.Wrap(err, "inferring JDK from manifest")
	}
	// If we could not determine the build JDK from the manifest, use target JDK if its JDK URL is known.
	// Else, fall back to a default JDK version.
	if JDKDownloadURLs[buildJDK] == "" {
		if JDKDownloadURLs[targetJDK] != "" {
			// buildJDK >= targetJDK so we can use targetJDK as buildJDK
			buildJDK = targetJDK
		} else {
			buildJDK = fallbackJDK
		}
	}
	return buildJDK, targetJDK, nil
}

// inferJDKFromManifest extracts the JDK version from the MANIFEST.MF file in the JAR.
func inferJDKFromManifest(zipReader *zip.Reader) (string, error) {
	manifestFile, err := zipReader.Open("META-INF/MANIFEST.MF")
	if err != nil {
		return "", errors.Wrap(err, "opening manifest file")
	}
	defer manifestFile.Close()

	manifestReader, err := io.ReadAll(manifestFile)
	if err != nil {
		return "", errors.Wrap(err, "reading manifest file")
	}
	for _, line := range strings.Split(string(manifestReader), "\n") {
		if strings.HasPrefix(line, "Build-Jdk:") || strings.HasPrefix(line, "Build-Jdk-Spec:") {
			_, value, _ := strings.Cut(line, ":")
			return strings.TrimSpace(value), nil
		}
	}
	return "", nil
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
func findClosestCommitToSource(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repo *git.Repository) (*object.Commit, error) {
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
	searchStrategy := gitscan.ExactTreeCount{}
	closest, matched, total, err := searchStrategy.Search(ctx, repo, hashes)
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
	commitHash := plumbing.NewHash(commitString)
	commitObject, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, errors.Wrapf(err, "resolving commit %s", commitString)
	}
	return commitObject, nil
}
