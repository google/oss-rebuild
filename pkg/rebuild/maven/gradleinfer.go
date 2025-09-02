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

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

type GradleBuildInferer struct{}

var _ rebuild.StrategyInferer = &GradleBuildInferer{}

func (m *GradleBuildInferer) Infer(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, repoConfig *rebuild.RepoConfig, commitObject *object.Commit) (rebuild.Strategy, error) {
	buildGradleDir, err := findBuildGradle(commitObject, t.Package)
	if err != nil {
		return nil, errors.Wrapf(err, "build manifest heuristic failed")
	}
	name, version := t.Package, t.Version
	tagGuess, err := rebuild.FindTagMatch(name, version, repoConfig.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "[INTERNAL] tag heuristic error")
	}

	var ref string
	if tagGuess != "" {
		ref = tagGuess
		log.Printf("using tag heuristic ref: %s", tagGuess[:9])
	} else {
		return nil, errors.Errorf("no valid git ref")
	}

	// Infer JDK for Gradle
	jdk, err := inferOrFallbackToDefaultJDK(ctx, name, version, mux)
	if err != nil {
		return nil, errors.Wrap(err, "fetching JDK")
	}

	return &GradleBuild{
		Location: rebuild.Location{
			Repo: repoConfig.URI,
			Dir:  buildGradleDir,
			Ref:  ref,
		},
		JDKVersion: jdk,
	}, nil
}

func findBuildGradle(commit *object.Commit, pkg string) (string, error) {
	commitTree, _ := commit.Tree()
	var candidateArtifactNames []string
	var topLevelGroupID string
	commitTree.Files().ForEach(func(f *object.File) error {
		// Skip files in gradle/ directory
		// This directory often contains wrapper scripts and other configuration files
		// https://docs.gradle.org/current/userguide/gradle_directories.html
		if strings.HasPrefix(f.Name, "gradle/") {
			return nil
		}
		if (strings.HasSuffix(f.Name, ".gradle") || strings.HasSuffix(f.Name, ".gradle.kts")) && !strings.HasPrefix(f.Name, "src/") && !strings.Contains(f.Name, "/src/") {
			candidateArtifactNames = append(candidateArtifactNames, path.Dir(f.Name))

			parseAndSetGroupID(f, &topLevelGroupID)
		}
		if f.Name == "gradle.properties" {
			parseAndSetGroupID(f, &topLevelGroupID)
		}
		return nil
	})
	if len(candidateArtifactNames) == 0 {
		return "", errors.New("no valid build.gradle found")
	}

	if topLevelGroupID == "" {
		log.Printf("no group ID found in build.gradle or gradle.properties; proceeding without it")
	}
	log.Printf("Top-level group ID: %s", topLevelGroupID)

	minDist := math.MaxInt
	bestMatch := ""
	for _, candidate := range candidateArtifactNames {
		combinedName := fmt.Sprintf("%s:%s", topLevelGroupID, path.Base(candidate))
		dist := minEditDistance(combinedName, pkg)
		if dist == 0 {
			log.Printf("Found exact match for Gradle project: %s", combinedName)
			return candidate, nil
		}
		if dist < minDist {
			minDist = dist
			bestMatch = candidate
		}
	}
	log.Printf("Found best match with minimum edit distance: %s (distance %d)", bestMatch, minDist)
	return bestMatch, nil
}

func parseAndSetGroupID(f *object.File, topLevelGroupID *string) {
	if *topLevelGroupID != "" {
		return
	}
	content, err := f.Contents()
	if err != nil {
		return
	}
	regex := regexp.MustCompile(`group\s*=?\s*['"]?([^'"\s]+)['"]?`)
	matcher := regex.FindStringSubmatch(content)
	if len(matcher) > 1 {
		*topLevelGroupID = matcher[1]
	}
}

func minEditDistance(s1, s2 string) int {
	len1 := len(s1)
	len2 := len(s2)
	dp := make([][]int, len1+1)
	for i := range dp {
		dp[i] = make([]int, len2+1)
	}

	for i := 0; i <= len1; i++ {
		dp[i][0] = i
	}
	for j := 0; j <= len2; j++ {
		dp[0][j] = j
	}

	for i := 1; i <= len1; i++ {
		for j := 1; j <= len2; j++ {
			if s1[i-1] == s2[j-1] {
				dp[i][j] = dp[i-1][j-1]
			} else {
				dp[i][j] = 1 + min(dp[i-1][j], dp[i][j-1], dp[i-1][j-1])
			}
		}
	}

	return dp[len1][len2]
}
