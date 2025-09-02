// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"testing"

	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
)

func TestFindBuildGradleDir(t *testing.T) {
	testCases := []struct {
		name        string
		repo        string
		pkg         string
		expectedDir string
		wantErr     bool
	}{
		{
			name: "single build.gradle at root",
			repo: `
            commits:
                - id: inital-commit
                  files:
                    build.gradle: |
                        group='com.example'`,
			pkg:         "com.example:myapp",
			expectedDir: ".",
			wantErr:     false,
		},
		{
			name: "single build.gradle.kts at root",
			repo: `
            commits:
                - id: inital-commit
                  files:
                    build.gradle.kts: |
                        group='org.sample'`,
			pkg:         "org.sample:lib",
			expectedDir: ".",
			wantErr:     false,
		},
		{
			name: "throw error when there are no valid build gradle files",
			repo: `
            commits:
                - id: inital-commit
                  files:
                    README.md: |
                        Sample Project
                    src/main/java/App.java: |
                        package main;`,
			pkg:         "com.example:anotherproject",
			expectedDir: "",
			wantErr:     true,
		},
		{
			name: "multiple build.gradle files, select exact match",
			repo: `
            commits:
                - id: inital-commit
                  files:
                    subproject/build.gradle: |
                        group='com.example'
                    anotherproject/build.gradle.kts: |
                        group='com.example'
                    build.gradle: |
                        group='com.example'`,
			pkg:         "com.example:anotherproject",
			expectedDir: "anotherproject",
			wantErr:     false,
		},
		{
			name: "multiple build.gradle files, no exact match",
			repo: `
            commits:
                - id: inital-commit
                  files:
                    api/build.gradle: |
                        group='com.example'
                    impl/build.gradle.kts: |
                        group='com.example'
                    build.gradle: |
                        group='com.example'`,
			pkg:         "com.example:example-api",
			expectedDir: "api",
			wantErr:     false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repo, nil))
			head, _ := repo.Head()
			headCommit, _ := repo.CommitObject(head.Hash())
			actualDir, err := findBuildGradleDir(headCommit, tc.pkg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("findBuildGradleDir() should fail but did not")
				}
			} else {
				if err != nil {
					t.Fatalf("findBuildGradleDir() error = %v", err)
				}
				if actualDir != tc.expectedDir {
					t.Errorf("findBuildGradleDir() = %q, want %q", actualDir, tc.expectedDir)
				}
			}
		})
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
