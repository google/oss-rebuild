// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
)

type mockMavenRegistry struct {
	maven.Registry
	artifactCoordinates map[struct{ PackageName, VersionID, FileType string }][]byte
	releaseFileError    error
}

func (m *mockMavenRegistry) ReleaseFile(ctx context.Context, name string, version string, fileType string) (io.ReadCloser, error) {
	if m.releaseFileError != nil {
		return nil, m.releaseFileError
	}
	return io.NopCloser(bytes.NewReader(m.artifactCoordinates[struct{ PackageName, VersionID, FileType string }{PackageName: name, VersionID: version, FileType: fileType}])), nil
}

func TestJDKVersionInference(t *testing.T) {
	testCases := []struct {
		name        string
		input       []*archive.ZipEntry
		wantVersion string
	}{
		{
			name: "build-jdk-spec attribute from manifest",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\nBuild-Jdk-Spec: 17.0.1\r\n\r\n"),
				},
			},
			wantVersion: "17.0.1",
		},
		{
			name: "build-jdk attribute from manifest",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\nBuild-Jdk: 21.0.1\r\n\r\n"),
				},
			},
			wantVersion: "21.0.1",
		},
		{
			name: "manifest takes precedence",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\nBuild-Jdk-Spec: 17.0.2\r\n\r\n"),
				},
				{
					FileHeader: &zip.FileHeader{Name: "com/example/Main.class"},
					Body:       []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x34, 0x01, 0x02},
				},
			},
			wantVersion: "17.0.2",
		},
		{
			name: "fallback to classfile",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\n\r\n"),
				},
				{
					FileHeader: &zip.FileHeader{Name: "com/example/Main.class"},
					Body:       []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x37, 0x01, 0x02},
				},
			},
			wantVersion: "11",
		},
		{
			name: "fallback to default JDK version",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\nBuild-Jdk-Spec: 1.8.0_121\r\n\r\n"),
				},
			},
			wantVersion: "11",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			for _, entry := range tc.input {
				if err := entry.WriteTo(zw); err != nil {
					t.Fatalf("WriteTo() error: %v", err)
				}
			}
			if err := zw.Close(); err != nil {
				t.Fatalf("zip.Close() error: %v", err)
			}

			mockMux := rebuild.RegistryMux{
				Maven: &mockMavenRegistry{
					artifactCoordinates: map[struct{ PackageName, VersionID, FileType string }][]byte{
						{"dummy", "dummy", maven.TypeJar}: buf.Bytes(),
					},
				},
			}
			got, err := inferOrFallbackToDefaultJDK(context.Background(), "dummy", "dummy", mockMux)
			if err != nil {
				t.Fatalf("getJarJDK() error = %v", err)
			}
			if got != tc.wantVersion {
				t.Errorf("JDK version = %v, want %v", got, tc.wantVersion)
			}
		})
	}
}

func TestGetClassFileMajorVersion(t *testing.T) {
	testCases := []struct {
		name       string
		classBytes []byte
		want       int
		wantErr    bool
	}{
		{
			name: "Valid Java 8 class file",
			// Magic, minor, major version 52 (0x34)
			classBytes: []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x34, 0x01, 0x02},
			want:       8,
			wantErr:    false,
		},
		{
			name: "Valid Java 11 class file",
			// Magic, minor, major version 55 (0x37)
			classBytes: []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x37, 0x01, 0x02},
			want:       11,
			wantErr:    false,
		},
		{
			name:       "File too short",
			classBytes: []byte{0xCA, 0xFE, 0xBA, 0xBE},
			wantErr:    true,
		},
		{
			name:       "Invalid magic number",
			classBytes: []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x34},
			wantErr:    true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := getClassFileMajorVersion(tc.classBytes)
			if (err != nil) != tc.wantErr {
				t.Errorf("getClassFileMajorVersion() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if got != tc.want {
				t.Errorf("getClassFileMajorVersion() = %v, want %v", got, tc.want)
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
