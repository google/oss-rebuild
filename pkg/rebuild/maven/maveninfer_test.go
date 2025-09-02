// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
)

func TestFindPomXMLHeuristic(t *testing.T) {
	testCases := []struct {
		name        string
		poms        []string
		expectedPom string
	}{
		{
			name:        "single pom.xml at root",
			poms:        []string{"pom.xml"},
			expectedPom: "pom.xml",
		},
		{
			name:        "select pom.xml in subdir",
			poms:        []string{"subdir/pom.xml"},
			expectedPom: "subdir/pom.xml",
		},
		{
			name:        "select a non-test resource pom.xml",
			poms:        []string{"moduleA/pom.xml", "src/test/resource/pom.xml"},
			expectedPom: "moduleA/pom.xml",
		},
		{
			name:        "select pom.xml with basename match",
			poms:        []string{"moduleA/test-pom.xml", "moduleB/pom.xml"},
			expectedPom: "moduleB/pom.xml",
		},
		{
			name:        "select pom.xml with src prefix in directory name",
			poms:        []string{"srcmoduleA/pom.xml", "src/moduleB/pom.xml"},
			expectedPom: "srcmoduleA/pom.xml",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fs := memfs.New()
			repo, _ := git.Init(memory.NewStorage(), fs)
			w, _ := repo.Worktree()
			for _, pomPath := range tc.poms {
				dir := path.Dir(pomPath)
				if dir != "." {
					fs.MkdirAll(dir, 0755)
				}
				// Write a minimal valid pom.xml content to memfs
				f, err := fs.Create(pomPath)
				if err != nil {
					t.Fatalf("failed to create file: %v", err)
				}
				content := `<project><groupId>group</groupId><artifactId>artifact</artifactId><version>1.0.0</version></project>`
				_, err = f.Write([]byte(content))
				if err != nil {
					t.Fatalf("failed to write file: %v", err)
				}
				f.Close()
				w.Add(pomPath)
			}
			commitHash, _ := w.Commit("add multiple pom files", &git.CommitOptions{
				Author: &object.Signature{
					Name:  "Foo Bar",
					Email: "foo@bar",
					When:  time.Now(),
				},
			})
			commit, _ := repo.CommitObject(commitHash)
			_, name, err := findPomXML(commit, "group:artifact")
			if err != nil {
				t.Fatalf("findPomXML() error = %v", err)
			}
			if name != tc.expectedPom {
				t.Errorf("findPomXML() = %q, want %q", name, tc.expectedPom)
			}
		})
	}
}

func TestSourceRepositoryURLInference(t *testing.T) {
	testCases := []struct {
		name        string
		pom         []PomXML
		targetPom   rebuild.Target
		expectedURL string
		wantErr     bool
	}{
		{
			name: "get SCM URL",
			pom: []PomXML{
				{
					GroupID:    "org.example",
					ArtifactID: "child",
					VersionID:  "1.0.0",
					SCMURL:     "https://github.com/example/child",
					URL:        "https://example.com/child",
				},
			},
			targetPom: rebuild.Target{
				Package: "org.example:child",
				Version: "1.0.0",
			},
			expectedURL: "https://github.com/example/child",
			wantErr:     false,
		},
		{
			name: "get URL",
			pom: []PomXML{
				{
					GroupID:    "com.example",
					ArtifactID: "child",
					VersionID:  "1.0.0",
					URL:        "https://example.com/child",
				},
			},
			targetPom: rebuild.Target{
				Package: "com.example:child",
				Version: "1.0.0",
			},
			expectedURL: "https://example.com/child",
			wantErr:     false,
		},
		{
			name: "get parent SCM URL",
			pom: []PomXML{
				{
					GroupID:    "com.example",
					ArtifactID: "child",
					VersionID:  "1.0.0",
					Parent: Parent{
						GroupID:    "com.example.parent",
						ArtifactID: "parent",
						VersionID:  "1.0.0",
					},
				},
				{
					GroupID:    "com.example.parent",
					ArtifactID: "parent",
					VersionID:  "1.0.0",
					SCMURL:     "https://github.com/example/parent",
				},
			},
			targetPom: rebuild.Target{
				Package: "com.example:child",
				Version: "1.0.0",
			},
			expectedURL: "https://github.com/example/parent",
			wantErr:     false,
		},
		{
			name: "get URL of parent and not child",
			pom: []PomXML{
				{
					GroupID:    "com.example",
					ArtifactID: "child",
					VersionID:  "1.0.0",
					URL:        "https://example.com/child",
					Parent: Parent{
						GroupID:    "com.example.parent",
						ArtifactID: "parent",
						VersionID:  "1.0.0",
					},
				},
				{
					GroupID:    "com.example.parent",
					ArtifactID: "parent",
					VersionID:  "1.0.0",
					URL:        "https://example.com/parent",
				},
			},
			targetPom: rebuild.Target{
				Package: "com.example:child",
				Version: "1.0.0",
			},
			expectedURL: "https://example.com/parent",
			wantErr:     false,
		},
		{
			name: "should throw an error for no valid URL",
			pom: []PomXML{
				{
					GroupID:    "com.example",
					ArtifactID: "child",
					VersionID:  "1.0.0",
					Parent: Parent{
						GroupID:    "com.example.parent",
						ArtifactID: "parent",
						VersionID:  "1.0.0",
					},
				},
				{
					GroupID:    "com.example.parent",
					ArtifactID: "parent",
					VersionID:  "1.0.0",
				},
			},
			targetPom: rebuild.Target{
				Package: "com.example:child",
				Version: "1.0.0",
			},
			expectedURL: "",
			wantErr:     true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockRegistry := &mockMavenRegistry{
				artifactCoordinates: make(map[struct{ PackageName, VersionID, FileType string }][]byte),
			}
			for _, pom := range tc.pom {
				addPomArtifact(mockRegistry, &pom)
			}
			mockMux := rebuild.RegistryMux{
				Maven: mockRegistry,
			}
			got, err := Rebuilder{}.InferRepo(context.Background(), tc.targetPom, mockMux)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("InferRepo() = %q, want error", got)
				}
			} else {
				if err != nil {
					t.Fatalf("InferRepo() error = %v", err)
				}
				if got != tc.expectedURL {
					t.Errorf("InferRepo() = %q, want %q", got, tc.expectedURL)
				}
			}

		})
	}
}

func addPomArtifact(mavenRegistry *mockMavenRegistry, pom *PomXML) {
	key := struct{ PackageName, VersionID, FileType string }{
		PackageName: fmt.Sprintf("%s:%s", pom.GroupID, pom.ArtifactID),
		VersionID:   pom.VersionID,
		FileType:    maven.TypePOM,
	}
	xml := "<project xmlns=\"http://maven.apache.org/POM/4.0.0\" xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" xsi:schemaLocation=\"http://maven.apache.org/POM/4.0.0 http://maven.apache.org/xsd/maven-4.0.0.xsd\"><groupId>" + pom.GroupID + "</groupId><artifactId>" + pom.ArtifactID + "</artifactId><version>" + pom.VersionID + "</version>"
	// Add parent if present
	if pom.Parent.GroupID != "" && pom.Parent.ArtifactID != "" && pom.Parent.VersionID != "" {
		xml += "<parent><groupId>" + pom.Parent.GroupID + "</groupId><artifactId>" + pom.Parent.ArtifactID + "</artifactId><version>" + pom.Parent.VersionID + "</version></parent>"
	}
	xml += "<url>" + pom.URL + "</url><scm><url>" + pom.SCMURL + "</url></scm></project>"
	mavenRegistry.artifactCoordinates[key] = []byte(xml)
}
