// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"fmt"
	"log"
	"math"
	"path"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// We use Gradle 8.14.3 as it supports running JDK versions 8-24.
// Reference: https://docs.gradle.org/current/userguide/compatibility.html
const gradleVersion = "8.14.3"

func GradleInfer(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repoConfig *rebuild.RepoConfig) (rebuild.Strategy, error) {
	tagGuess, err := rebuild.FindTagMatch(t.Package, t.Version, repoConfig.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	sourceJarGuess, err := findClosestCommitToSource(ctx, t, mux, repoConfig.Repository)
	if err != nil {
		log.Printf("source jar heuristic failed: %s", err)
	}
	var ref string
	switch {
	case tagGuess != "":
		ref = tagGuess
		log.Printf("using tag heuristic ref: %s", tagGuess[:9])
	case sourceJarGuess != nil:
		ref = sourceJarGuess.Hash.String()
		log.Printf("using source jar heuristic ref: %s", ref[:9])
	default:
		return nil, errors.Errorf("no git ref")
	}
	commitObject, err := repoConfig.Repository.CommitObject(plumbing.NewHash(ref))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to resolve commit object [repo=%s,ref=%s]", repoConfig.URI, ref)
	}
	buildGradleDir, err := findBuildGradleDir(commitObject, t.Package)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to find build.gradle directory [repo=%s,ref=%s]", repoConfig.URI, ref)
	}
	// Infer JDK for Gradle
	// Note: There does not appear to be a way to set target JDK version via command line args so we just infer the build JDK.
	// Reference: https://docs.gradle.org/current/userguide/building_java_projects.html#sec:java_cross_compilation
	buildJdk, _, err := inferJDKAndTargetVersion(ctx, t.Package, t.Version, mux)
	if err != nil {
		return nil, errors.Wrap(err, "fetching JDK")
	}
	// Check if gradlew is present in the commit
	var systemGradle string
	hasGradleWrapper, err := isGradleWrapperPresent(commitObject)
	if err != nil {
		return nil, errors.Wrap(err, "checking for gradle wrapper")
	}
	if !hasGradleWrapper {
		systemGradle = gradleVersion
		log.Printf("Gradle wrapper (gradlew) not found in the repository. Will use system-installed Gradle.")
	}
	return &GradleBuild{
		Location: rebuild.Location{
			Repo: repoConfig.URI,
			Dir:  buildGradleDir,
			Ref:  ref,
		},
		JDKVersion:   buildJdk,
		SystemGradle: systemGradle,
	}, nil
}

func isGradleWrapperPresent(commit *object.Commit) (bool, error) {
	tree, err := commit.Tree()
	if err != nil {
		return false, errors.Wrap(err, "getting tree")
	}
	_, err = tree.FindEntry("gradlew")
	if err != nil {
		return false, nil
	}
	return true, nil
}

func findBuildGradleDir(commit *object.Commit, pkg string) (string, error) {
	commitTree, _ := commit.Tree()
	var candidateDirs []string
	var topLevelGroupID string
	// In a typical multi-module project, the root project's build.gradle defines groupId for the entire project.
	// This parent groupId is then inherited by all sub-modules.
	// This structure ensures that all related modules are grouped under a common namespace, simplifying dependency management and versioning across the project.
	minDepth := math.MaxInt
	err := commitTree.Files().ForEach(func(f *object.File) error {
		// Skip files in gradle/, src/, or any subdirectory containing src/.
		// gradle directory often contains wrapper scripts and other configuration files.
		// https://docs.gradle.org/current/userguide/gradle_directories.html
		// src/ is typically used for source code, not build configuration.
		if strings.HasPrefix(f.Name, "gradle/") || strings.HasPrefix(f.Name, "src/") || strings.Contains(f.Name, "/src/") {
			return nil
		}
		depth := strings.Count(f.Name, "/")
		if depth >= minDepth {
			return nil
		}
		var err error
		// Look for build.gradle or build.gradle.kts files to identify potential project directories.
		// Also check gradle.properties for group ID.
		if strings.HasSuffix(f.Name, ".gradle") || strings.HasSuffix(f.Name, ".gradle.kts") {
			candidateDirs = append(candidateDirs, path.Dir(f.Name))
			topLevelGroupID, err = getGroupIDFromFile(f)
			if err != nil {
				return err
			}
		}
		if f.Name == "gradle.properties" {
			topLevelGroupID, err = getGroupIDFromFile(f)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", errors.Wrap(err, "traversing through files in commit")
	} else if topLevelGroupID == "" {
		log.Printf("No top-level group ID found in Gradle files")
	}
	if len(candidateDirs) == 0 {
		return "", errors.New("no valid build.gradle found")
	}
	// Find the candidate with the minimum edit distance to the artifact name
	minDist := math.MaxInt
	// Default to the root directory if no better match is found
	bestMatch := "."
	for _, candidate := range candidateDirs {
		combinedName := fmt.Sprintf("%s:%s", topLevelGroupID, path.Base(candidate))
		dist := minEditDistance(combinedName, pkg)
		if dist == 0 {
			log.Printf("Found exact match for Gradle project: %s", combinedName)
			return candidate, nil
		}
		_, a, _ := strings.Cut(pkg, ":")
		if dist <= minDist && strings.Contains(a, path.Base(candidate)) {
			minDist = dist
			bestMatch = candidate
		}
	}
	log.Printf("Found best match with minimum edit distance: %s (distance %d)", bestMatch, minDist)
	return bestMatch, nil
}

func getGroupIDFromFile(f *object.File) (string, error) {
	content, err := f.Contents()
	if err != nil {
		return "", errors.Wrapf(err, "reading file %s", f.Name)
	}
	var groupIDRegex = regexp.MustCompile(`(?m)^\s*<groupId>([^<]+?)</groupId>`)
	matcher := groupIDRegex.FindStringSubmatch(content)
	if len(matcher) > 1 {
		return matcher[1], nil
	}
	return "", nil
}

// minEditDistance computes the Levenshtein distance between two strings.
func minEditDistance(s1, s2 string) int {
	len1 := len(s1)
	len2 := len(s2)
	dp := make([][]int, len1+1)
	for i := range dp {
		dp[i] = make([]int, len2+1)
	}

	for i := 0; i < len1+1; i++ {
		dp[i][0] = i
	}
	for j := 0; j < len2+1; j++ {
		dp[0][j] = j
	}

	for i := 1; i < len1+1; i++ {
		for j := 1; j < len2+1; j++ {
			if s1[i-1] == s2[j-1] {
				dp[i][j] = dp[i-1][j-1]
			} else {
				dp[i][j] = 1 + min(dp[i-1][j], dp[i][j-1], dp[i-1][j-1])
			}
		}
	}

	return dp[len1][len2]
}
