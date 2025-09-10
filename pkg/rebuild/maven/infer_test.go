// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
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
			wantVersion: "8",
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

func TestBuildToolInference(t *testing.T) {
	for _, tc := range []struct {
		name              string
		repo              string
		expectedBuildTool string
		wantErr           bool
	}{
		{
			name: "pom.xml present",
			repo: `
            commits:
              - id: initial-commit
                files:
                  pom.xml: |
                      <project></project>`,
			expectedBuildTool: mavenBuildTool,
			wantErr:           false,
		},
		{
			name: "pom.xml absent",
			repo: `
            commits:
              - id: initial-commit
                files:
                  README.md: |
                      # Sample Project`,
			expectedBuildTool: "",
			wantErr:           true,
		},
		{
			name: "pom from src directory should be ignored",
			repo: `
            commits:
              - id: initial-commit
                files:
                  src/test/resources/pom.xml: |
                      <project></project>`,
			expectedBuildTool: mavenBuildTool,
			wantErr:           true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repo, nil))
			head := must(repo.Head())
			headCommit := must(repo.CommitObject(head.Hash()))
			buildTool, err := inferBuildTool(headCommit)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("inferBuildTool() expected error but got none")
				}
			} else {
				if err != nil {
					t.Fatalf("inferBuildTool() error = %v", err)
				}
				if buildTool != tc.expectedBuildTool {
					t.Errorf("inferBuildTool() = %v, want %v", buildTool, tc.expectedBuildTool)
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
