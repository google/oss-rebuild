// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"log"
	"math"
	"path"
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
	var ref string
	// 1. Tag Heuristic
	tagGuess, err := rebuild.FindTagMatch(t.Package, t.Version, repoConfig.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}
	if tagGuess != "" {
		ref = tagGuess
		log.Printf("using tag heuristic ref: %s", tagGuess[:9])
	}
	// 2. Source Jar Heuristic
	if ref == "" {
		sourceJarGuess, err := findClosestCommitToSource(ctx, t, mux, repoConfig.Repository)
		if err != nil {
			log.Printf("source jar heuristic failed: %s", err)
		} else if sourceJarGuess != nil {
			ref = sourceJarGuess.Hash.String()
			log.Printf("using source jar heuristic ref: %s", ref[:9])
		}
	}
	if ref == "" {
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
	jdk, err := inferOrFallbackToDefaultJDK(ctx, t.Package, t.Version, mux)
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
		JDKVersion:   jdk,
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
		// Look for build.gradle or build.gradle.kts files to identify potential project directories.
		if strings.HasSuffix(f.Name, ".gradle") || strings.HasSuffix(f.Name, ".gradle.kts") {
			candidateDirs = append(candidateDirs, path.Dir(f.Name))
		}
		return nil
	})
	if err != nil {
		return "", errors.Wrap(err, "traversing through files in commit")
	}
	if len(candidateDirs) == 0 {
		return "", errors.New("no valid build.gradle found")
	}
	// Find the candidate with the minimum edit distance to the artifact name
	minDist := math.MaxInt
	// Default to the root directory if no better match is found
	bestMatch := "."
	for _, candidate := range candidateDirs {
		dist := minEditDistance(path.Base(candidate), pkg)
		if dist == 0 {
			log.Printf("Found exact match for Gradle project: %s", path.Base(candidate))
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
