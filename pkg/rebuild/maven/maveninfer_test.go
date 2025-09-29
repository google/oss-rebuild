// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"log"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/archive"
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
				artifactCoordinates: make(map[artifactCoordinates][]byte),
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
	key := artifactCoordinates{
		PackageName: fmt.Sprintf("%s:%s", pom.GroupID, pom.ArtifactID),
		VersionID:   pom.VersionID,
		FileType:    maven.TypePOM,
	}
	xmlBytes, _ := xml.MarshalIndent(pom, "", "  ")
	mavenRegistry.artifactCoordinates[key] = xmlBytes
}

func TestMavenInfer(t *testing.T) {
	testCases := []struct {
		name              string
		target            rebuild.Target
		repo              string
		zipEntries        map[string][]*archive.ZipEntry
		expectedHeuristic string
		wantErr           bool
	}{
		{
			name: "infer using git log heuristic",
			target: rebuild.Target{
				Ecosystem: "Maven",
				Package:   "foo:bar",
				Version:   "1.0.0",
			},
			repo: `
            commits:
              - id: initial-commit
                files:
                  pom.xml: |
                    <project>
                        <modelVersion>4.0.0</modelVersion>
                        <groupId>foo</groupId>
                        <artifactId>bar</artifactId>
                        <version>1.0.0</version>
                    </project>`,
			zipEntries: map[string][]*archive.ZipEntry{
				maven.TypeSources: {{
					FileHeader: &zip.FileHeader{Name: "Foo.java"},
					Body:       []byte("class Foo {}"),
				}},
				maven.TypeJar: {
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/maven/foo/bar/pom.xml"},
						Body:       []byte(`<project><groupId>foo</groupId><artifactId>bar</artifactId><version>1.0.0</version></project>`),
					},
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
						Body:       []byte("Manifest-Version: 1.0\nBuild-Jdk: 11.0.1\n"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "com/example/Foo.class"},
						Body:       []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x34},
					},
				},
			},
			expectedHeuristic: "using git log heuristic (pkg and version match)",
			wantErr:           false,
		},
		{
			name: "infer using source jar heuristic",
			target: rebuild.Target{
				Ecosystem: "Maven",
				Package:   "foo:bar",
				Version:   "1.0.0",
			},
			repo: `
            commits:
              - id: initial-commit
                files:
                  pom.xml: |
                    <project>
                        <modelVersion>4.0.0</modelVersion>
                        <groupId>foo</groupId>
                        <artifactId>bar</artifactId>
                        <version>0.0.0-dev</version>
                    </project>
                  src/main/java/Foo.java: |
                    a`,
			zipEntries: map[string][]*archive.ZipEntry{
				maven.TypeSources: {{
					FileHeader: &zip.FileHeader{Name: "src/main/java/Foo.java"},
					Body:       []byte("a"),
				}},
				maven.TypeJar: {
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/maven/foo/bar/pom.xml"},
						Body:       []byte("<project><groupId>foo</groupId><artifactId>bar</artifactId><version>1.0.0</version></project>"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
						Body:       []byte("Manifest-Version: 1.0\nBuild-Jdk: 11.0.1\n"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "com/example/Foo.class"},
						Body:       []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x41},
					},
				},
			},
			expectedHeuristic: "using source jar heuristic with mismatched version",
			wantErr:           false,
		},
		{
			name: "infer using tag heuristic",
			target: rebuild.Target{
				Ecosystem: "Maven",
				Package:   "foo:bar",
				Version:   "1.0.0",
			},
			repo: `
            commits:
              - id: initial-commit
                tags: ["v1.0.0"]
                files:
                  pom.xml: |
                    <project>
                        <modelVersion>4.0.0</modelVersion>
                        <groupId>foo</groupId>
                        <artifactId>bar</artifactId>
                        <version>0.0.0-dev</version>
                    </project>`,
			zipEntries: map[string][]*archive.ZipEntry{
				maven.TypeSources: {{
					FileHeader: &zip.FileHeader{Name: "src/main/java/Foo.java"},
					Body:       []byte("a"),
				}},
				maven.TypeJar: {
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/maven/foo/bar/pom.xml"},
						Body:       []byte("<project><groupId>foo</groupId><artifactId>bar</artifactId><version>1.0.0</version></project>"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
						Body:       []byte("Manifest-Version: 1.0\nBuild-Jdk: 11.0.1\n"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "com/example/Foo.class"},
						Body:       []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x41},
					},
				},
			},
			expectedHeuristic: "using tag heuristic with mismatched version",
			wantErr:           false,
		},
		{
			name: "prevent checking for source jar heuristic if it is nil",
			target: rebuild.Target{
				Ecosystem: "Maven",
				Package:   "foo:bar",
				Version:   "1.0.0",
			},
			repo: `
            commits:
              - id: initial-commit
                tags: ["1.0.0"]
                files:
                  pom.xml: |
                    <project>
                        <modelVersion>4.0.0</modelVersion>
                        <groupId>blah</groupId>
                        <artifactId>blah</artifactId>
                        <version>0.0.0-dev</version>
                    </project>`,
			zipEntries: map[string][]*archive.ZipEntry{
				maven.TypeJar: {
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/maven/foo/bar/pom.xml"},
						Body:       []byte("<project><groupId>foo</groupId><artifactId>bar</artifactId><version>1.0.0</version></project>"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
						Body:       []byte("Manifest-Version: 1.0\nBuild-Jdk: 11.0.1\n"),
					},
					{
						FileHeader: &zip.FileHeader{Name: "com/example/Foo.class"},
						Body:       []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x42},
					},
				},
			},
			expectedHeuristic: "",
			// throw no valid git ref as tag matches but then package does not match
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
			capturedStderr := &bytes.Buffer{}
			log.SetOutput(capturedStderr)
			defer func() {
				log.SetOutput(nil)
			}()
			got, err := MavenInfer(context.Background(), tc.target, mockMux, repoConfig)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("MavenInfer() = %v, want error", got)
				}
			} else {
				if err != nil {
					t.Fatalf("MavenInfer() error = %v", err)
				}
				if !strings.Contains(capturedStderr.String(), tc.expectedHeuristic) {
					t.Errorf("MavenInfer() did not use expected heuristic, got logs: %s", capturedStderr.String())
				}
			}

		})
	}
}

func addArtifacts(mavenRegistry *mockMavenRegistry, entries map[string][]*archive.ZipEntry, target rebuild.Target) error {
	for artifactType, files := range entries {
		buf := bytes.Buffer{}
		zipWriter := zip.NewWriter(&buf)
		for _, entry := range files {
			if err := entry.WriteTo(zipWriter); err != nil {
				panic(err)
			}
		}
		if err := zipWriter.Close(); err != nil {
			panic(err)
		}
		key := artifactCoordinates{
			PackageName: target.Package,
			VersionID:   target.Version,
			FileType:    artifactType,
		}
		mavenRegistry.artifactCoordinates[key] = buf.Bytes()
	}
	return nil
}
