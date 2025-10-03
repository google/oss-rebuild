// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"archive/zip"
	"testing"

	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
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
			actualDir, _, err := findBuildGradleDir(headCommit, tc.pkg)
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

func TestGradleInfer(t *testing.T) {
	testCases := []struct {
		name           string
		target         rebuild.Target
		repo           string
		zipEntries     map[string][]*archive.ZipEntry
		expectedCommit string
		wantErr        bool
	}{
		{
			name: "infer using tag heuristic",
			target: rebuild.Target{
				Package:   "com.example:myapp",
				Version:   "1.0.0",
				Ecosystem: rebuild.Maven,
			},
			repo: `
            commits:
            - id: initial-commit
              files:
                gradle.properties: |
                  group=com.example
                build.gradle: |
                  repositories {
                    mavenCentral()
                  }
            - id: second-commit
              parents: [initial-commit]
              tags: ['v1.0.0']
              files:
                README.md: |
                    Sample Project`,
			zipEntries: map[string][]*archive.ZipEntry{
				maven.TypeJar: {
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/maven/com/example/myapp/pom.xml"},
						Body:       []byte("<project><groupId>com.example</groupId><artifactId>myapp</artifactId><version>1.0.0</version></project>"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
						Body:       []byte("Manifest-Version: 1.0\nBuild-Jdk: 11.0.1\n"),
					},
				},
			},
			// select second-commit because it has the tag matching version 1.0.0
			expectedCommit: "second-commit",
		},
		{
			name: "infer using source jar heuristic",
			target: rebuild.Target{
				Package:   "com.example:myapp",
				Version:   "1.0.0",
				Ecosystem: rebuild.Maven,
			},
			repo: `
            commits:
            - id: initial-commit
              files:
                build.gradle: |
                  group=com.example
                src/main/java/App.java: |
                  class App {}
            - id: second-commit
              parents: [initial-commit]
              files:
                build.gradle: |
                  group=com.example
                src/main/java/App.java: |
                  public class App {}`,
			zipEntries: map[string][]*archive.ZipEntry{
				maven.TypeJar: {
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/maven/com/example/myapp/pom.xml"},
						Body:       []byte("<project><groupId>com.example</groupId><artifactId>myapp</artifactId><version>1.0.0</version></project>"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
						Body:       []byte("Manifest-Version: 1.0\nBuild-Jdk: 11.0.1\n"),
					},
				},
				maven.TypeSources: {
					{
						FileHeader: &zip.FileHeader{Name: "src/main/java/App.java"},
						Body:       []byte("public class App {}"),
					},
				},
			},
			// select second-commit because it is the closest to the source jar creation time
			expectedCommit: "second-commit",
		},
		{
			name: "fail when no heuristics match",
			target: rebuild.Target{
				Package:   "com.example:unknown",
				Version:   "0.1.0",
				Ecosystem: rebuild.Maven,
			},
			repo: `
            commits:
            - id: initial-commit
              tags: ['4.2.0']
              files:
                build.gradle: |
                  group=com.example
                src/main/java/Main.java: |
                  public class Main {}`,
			zipEntries: map[string][]*archive.ZipEntry{
				maven.TypeJar: {
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/maven/com/example/unknown/pom.xml"},
						Body:       []byte("<project><groupId>com.example</groupId><artifactId>unknown</artifactId><version>0.1.0</version></project>"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
						Body:       []byte("Manifest-Version: 1.0\nBuild-Jdk: 11.0.1\n"),
					},
				},
			},
			wantErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repoConfig := &rebuild.RepoConfig{}
			repo, err := gitxtest.CreateRepoFromYAML(tc.repo, nil)
			if err != nil {
				t.Fatalf("CreateRepoFromYAML() error = %v", err)
			}
			repoConfig.Repository = repo.Repository
			mockRegistry := &mockMavenRegistry{
				artifactCoordinates: make(map[artifactCoordinates][]byte),
			}
			addArtifacts(mockRegistry, tc.zipEntries, tc.target)
			mockMux := rebuild.RegistryMux{
				Maven: mockRegistry,
			}
			got, err := GradleInfer(t.Context(), tc.target, mockMux, repoConfig)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("GradleInfer() = %v, want error", got)
				}
			} else {
				if err != nil {
					t.Fatalf("GradleInfer() error = %v", err)
				}
				actualCommit := got.(*GradleBuild).Location.Ref
				expectedCommit := repo.Commits[tc.expectedCommit].String()
				if actualCommit != expectedCommit {
					t.Errorf("GradleInfer() commit = %q, want %q", actualCommit, tc.expectedCommit)
				}
			}

		})
	}
}
