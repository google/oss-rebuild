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
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/oss-rebuild/internal/gitx"
	"github.com/google/oss-rebuild/internal/uri"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
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
		return MavenInfer(ctx, t, mux, repoConfig)
	case gradleBuildTool:
		return GradleInfer(ctx, t, mux, repoConfig)
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
		// TODO: strategy should install gradle if wrapper is not present
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

// inferOrFallbackToDefaultJDK tries to infer the JDK version from the artifact's metadata, falling back to a default if necessary.
func inferOrFallbackToDefaultJDK(ctx context.Context, name, version string, mux rebuild.RegistryMux) (string, error) {
	jdk, err := inferJDKVersion(ctx, name, version, mux)
	if err != nil {
		return "", errors.Wrap(err, "fetching JDK")
	}
	if jdk == "" {
		log.Printf("no JDK version inferred, falling back to JDK %s", fallbackJDK)
		jdk = fallbackJDK
	} else if JDKDownloadURLs[jdk] == "" {
		log.Printf("%s has no associated JDK URL, falling back to JDK %s", jdk, fallbackJDK)
		jdk = fallbackJDK
	}
	return jdk, nil
}

// inferJDKVersion gets the JDK version from the MANIFEST or Java bytecode.
func inferJDKVersion(ctx context.Context, name, version string, mux rebuild.RegistryMux) (string, error) {
	releaseFile, err := mux.Maven.ReleaseFile(ctx, name, version, maven.TypeJar)
	if err != nil {
		return "", errors.Wrap(err, "fetching jar file")
	}
	jarBytes, err := io.ReadAll(releaseFile)
	if err != nil {
		return "", errors.Wrap(err, "reading jar file")
	}
	zipReader, err := zip.NewReader(bytes.NewReader(jarBytes), int64(len(jarBytes)))
	if err != nil {
		return "", errors.Wrap(err, "unzipping jar file")
	}
	jdk, err := inferJDKFromManifest(zipReader)
	if err != nil {
		return "", errors.Wrap(err, "inferring JDK from manifest")
	}
	if jdk != "" {
		return jdk, nil
	}
	jdkInt, err := inferJDKFromBytecode(zipReader)
	if err != nil {
		return "", errors.Wrap(err, "inferring JDK from bytecode")
	}
	return fmt.Sprintf("%d", jdkInt), nil
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
func inferJDKFromBytecode(jarZip *zip.Reader) (int, error) {
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
				return 0, errors.Wrap(err, "parsing class file for major version")
			}
			return majorVersion, nil
		}
	}
	return 0, errors.New("no .class files found in jar")
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
