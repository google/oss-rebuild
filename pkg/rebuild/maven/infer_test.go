// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
)

type mockMavenRegistry struct {
	maven.Registry
	releaseFileContent io.ReadCloser
	releaseFileError   error
}

func (m *mockMavenRegistry) ReleaseFile(ctx context.Context, name string, version string, fileType string) (io.ReadCloser, error) {
	if m.releaseFileError != nil {
		return nil, m.releaseFileError
	}
	return m.releaseFileContent, nil
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
					releaseFileContent: io.NopCloser(bytes.NewReader(buf.Bytes())),
				},
			}
			got, err := getJarJDK(context.Background(), "dummy", "dummy", mockMux)
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
		name string
		pom  []PomXML
		url  string
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
			url: "https://github.com/example/child",
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
			url: "https://example.com/child",
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
			url: "https://github.com/example/parent",
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
					VersionID:  "2.0.0",
					URL:        "https://example.com/parent",
				},
			},
			url: "https://example.com/parent",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			for idx, pom := range tc.pom {
				mockMux := rebuild.RegistryMux{
					Maven: &mockMavenRegistry{
						releaseFileContent: io.NopCloser(strings.NewReader("<project xmlns=\"http://maven.apache.org/POM/4.0.0\" xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" xsi:schemaLocation=\"http://maven.apache.org/POM/4.0.0 http://maven.apache.org/xsd/maven-4.0.0.xsd\"><groupId>" + pom.GroupID + "</groupId><artifactId>" + pom.ArtifactID + "</artifactId><version>" + pom.VersionID + "</version><url>" + pom.URL + "</url><scm><url>" + pom.SCMURL + "</url></scm></project>")),
					},
				}
				got, err := Rebuilder{}.InferRepo(context.Background(), rebuild.Target{
					Ecosystem: rebuild.Maven,
					Package:   fmt.Sprintf("%s:%s", pom.GroupID, pom.ArtifactID),
					Version:   pom.VersionID,
				}, mockMux)
				if err != nil && idx == len(tc.pom)-1 {
					t.Fatalf("InferRepo() error = %v", err)
				}
				if got != tc.url && idx == len(tc.pom)-1 {
					t.Errorf("InferRepo() = %q, want %q", got, tc.url)
				}
			}

		})
	}
}
