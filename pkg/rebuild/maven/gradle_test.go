// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestRunPrintCoordinates(t *testing.T) {
	testCases := []struct {
		name         string
		repoURL      string
		sha          string
		expectedGAVs []string
	}{
		{
			name:         "GAV coordinates for a single module Gradle project",
			repoURL:      "https://github.com/chains-project/maven-module-graph.git",
			sha:          "32245d0a433e1a36d36c9fffa16b5936243e9c6b",
			expectedGAVs: []string{"io.algomaster99.maven_module_graph:maven-module-graph:1.0.0-SNAPSHOT"},
		},
		{
			name:    "GAV coordinates for a multi-module Gradle project",
			repoURL: "https://github.com/perfmark/perfmark",
			sha:     "9d9893c037949ec73ed9d018f6b9217b70d4bba6",
			expectedGAVs: []string{
				// "unspecified" version seems to be default value where no version is specified
				":perfmark:unspecified",
				"io.perfmark:perfmark-tracewriter:0.27.0",
				"io.perfmark:perfmark-java7:0.27.0",
				"io.perfmark:perfmark-api:0.27.0",
				"io.perfmark:perfmark-java6:0.27.0",
				"io.perfmark:perfmark-traceviewer:0.27.0",
				"io.perfmark:perfmark-java15:0.27.0",
				"io.perfmark:perfmark-java19:0.27.0",
				"io.perfmark:perfmark-api-testing:0.27.0",
				"io.perfmark:perfmark-impl:0.27.0",
				"io.perfmark:perfmark-java9:0.27.0",
				"io.perfmark:perfmark-agent:0.27.0",
				"io.perfmark:perfmark-examples:0.27.0",
				"io.perfmark:perfmark-testing:0.27.0"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir, err := os.MkdirTemp("", "gradle-test-")
			if err != nil {
				t.Fatalf("Failed to create temp directory: %v", err)
			}
			defer os.RemoveAll(tempDir)

			r, err := git.PlainClone(tempDir, false, &git.CloneOptions{URL: tc.repoURL})
			if err != nil {
				t.Fatalf("Failed to clone repository: %v", err)
			}
			wt, err := r.Worktree()
			if err != nil {
				t.Fatalf("Failed to get worktree: %v", err)
			}
			err = wt.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(tc.sha)})
			if err != nil {
				t.Fatalf("Failed to clone repository: %v", err)
			}
			gp, err := RunPrintCoordinates(*r)
			if err != nil {
				t.Fatalf("Failed to run printCoordinates: %v", err)
			}
			actualGAVs := getAllGAVs(gp)
			slices.Sort(actualGAVs)
			slices.Sort(tc.expectedGAVs)
			if !reflect.DeepEqual(actualGAVs, tc.expectedGAVs) {
				t.Fatalf("Expected GAVs %v, got %v", tc.expectedGAVs, actualGAVs)
			}
		})
	}
}

func getAllGAVs(gradleProject GradleProject) []string {
	var gavs []string
	gavs = append(gavs, fmt.Sprintf("%s:%s:%s", gradleProject.GroupId, gradleProject.ArtifactId, gradleProject.Version))
	for _, submodule := range gradleProject.Submodules {
		gavs = append(gavs, getAllGAVs(submodule)...)
	}
	return gavs
}
