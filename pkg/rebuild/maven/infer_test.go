// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/gitx/gitxtest"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
	"github.com/msuozzo/go-git2"
	"github.com/pkg/errors"
)

type artifactCoordinates struct{ PackageName, VersionID, FileType string }

type mockMavenRegistry struct {
	maven.Registry
	artifactCoordinates map[artifactCoordinates][]byte
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
			name: "use exact patch version when available",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\nBuild-Jdk-Spec: 17.0.6\r\n\r\n"),
				},
			},
			wantVersion: "17.0.6",
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
			name: "parse JDK 8 version format",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\nBuild-Jdk-Spec: 1.8.0_121\r\n\r\n"),
				},
			},
			wantVersion: "8u181",
		},
		{
			name: "build-jdk takes precedence over build-jdk-spec",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\nBuild-Jdk-Spec: 1.8\r\nBuild-Jdk: 1.8.0_342\r\n\r\n"),
				},
			},
			wantVersion: "8u342",
		},
		{
			name: "jdk from the jar tool's created-by",
			input: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					Body:       []byte("Manifest-Version: 1.0\r\nCreated-By: 11.0.4 (Oracle Corporation)\r\n\r\n"),
				},
			},
			wantVersion: "11.0.4",
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
					artifactCoordinates: map[artifactCoordinates][]byte{
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

func TestReadUpstreamJar(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []*archive.ZipEntry{
		{FileHeader: &zip.FileHeader{Name: "META-INF/MANIFEST.MF"}, Body: []byte("Manifest-Version: 1.0\r\nCreated-By: Apache Maven 3.5.4\r\nBuild-Jdk: 1.8.0_181\r\n\r\nName: x\r\nBuild-Jdk: 99\r\n")},
		{FileHeader: &zip.FileHeader{Name: "META-INF/maven/foo/bar/pom.xml"}, Body: []byte("<project>\r\n</project>\r\n")},
	} {
		if err := e.WriteTo(zw); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mux := rebuild.RegistryMux{Maven: &mockMavenRegistry{artifactCoordinates: map[artifactCoordinates][]byte{{"foo:bar", "1.0", maven.TypeJar}: buf.Bytes()}}}
	got, err := readUpstreamJar(context.Background(), "foo:bar", "1.0", mux)
	if err != nil {
		t.Fatal(err)
	}
	if want := (upstreamJar{JDK: "1.8.0_181", Maven: "3.5.4", CRLF: true}); got != want {
		t.Errorf("readUpstreamJar() = %+v, want %+v", got, want)
	}
}

func TestJDKFor(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"ExactUpdate", "1.8.0_181", "8u181"},
		{"NearestUpdate", "1.8.0_251", "8u252"},
		{"BuildSuffix", "1.8.0_181-b13", "8u181"},
		{"MajorOnly", "1.8", "8"},
		{"OlderThanAvailable", "1.7.0_79", "8"},
		{"ExactModern", "11.0.4", "11.0.4"},
		{"ModernBuildSuffix", "11.0.4+10-LTS", "11.0.4"},
		{"NearestModern", "17.0.99", "17.0.18"},
		{"ModernMajorOnly", "21", "21"},
		{"Empty", "", fallbackJDK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, jdkFor(tc.raw)); diff != "" {
				t.Errorf("jdkFor(%q) returned diff (-want +got):\n%s", tc.raw, diff)
			}
		})
	}
}

func TestReadUpstreamJarCorruptPom(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	m, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write([]byte("Build-Jdk: 11\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	p, err := zw.CreateRaw(&zip.FileHeader{Name: "META-INF/maven/g/a/pom.xml", CRC32: 1, CompressedSize64: 3, UncompressedSize64: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("pom")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mux := rebuild.RegistryMux{Maven: &mockMavenRegistry{artifactCoordinates: map[artifactCoordinates][]byte{{"dummy", "dummy", maven.TypeJar}: buf.Bytes()}}}
	if _, err := readUpstreamJar(context.Background(), "dummy", "dummy", mux); !errors.Is(err, zip.ErrChecksum) {
		t.Errorf("readUpstreamJar() error = %v, want %v", err, zip.ErrChecksum)
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
		{
			name: "gradle over maven",
			repo: `
            commits:
              - id: initial-commit
                files:
                  gradlew: |
                    #!/bin/sh
                  api/core/pom.xml: |
                    <project></project>`,
			expectedBuildTool: gradleBuildTool,
		},
		{
			name: "maven over gradle",
			repo: `
            commits:
              - id: initial-commit
                files:
                  pom.xml: |
                      <project></project>
                  api/core/gradlew: |
                      #!/bin/sh`,
			expectedBuildTool: mavenBuildTool,
		},
		{
			name: "sbt build file",
			repo: `
            commits:
              - id: initial-commit
                files:
                  build.sbt: |
                      name := "example"
                      version := "0.1.0"
                      scalaVersion := "2.13.6"`,
			expectedBuildTool: sbtBuildTool,
		},
		{
			name: "ant build file",
			repo: `
            commits:
              - id: initial-commit
                files:
                  build.xml: |
                      <project name="Example" default="compile">
                          <target name="compile">
                              <javac srcdir="src" destdir="build"/>
                          </target>
                      </project>`,
			expectedBuildTool: antBuildTool,
		},
		{
			name: "ivy build file",
			repo: `
            commits:
              - id: initial-commit
                files:
                  ivy.xml: |
                      <ivy-module version="2.0">
                          <info organisation="org.example" module="example"/>
                          <dependencies>
                              <dependency org="org.apache" name="commons-lang3" rev="3.12.0"/>
                          </dependencies>
                      </ivy-module>`,
			expectedBuildTool: ivyBuildTool,
		},
		{
			name: "leiningen build file",
			repo: `
            commits:
              - id: initial-commit
                files:
                  project.clj: |
                      (defproject example "0.1.0"
                        :description "An example Clojure project"
                        :dependencies [[org.clojure/clojure "1.10.3"]])`,
			expectedBuildTool: leiningenBuildTool,
		},
		{
			name: "npm build file",
			repo: `
            commits:
              - id: initial-commit
                files:
                  package.json: |
                      {
                        "name": "example",
                        "version": "1.0.0",
                        "main": "index.js",
                        "dependencies": {
                          "express": "^4.17.1"
                        }
                      }`,
			expectedBuildTool: npmBuildTool,
		},
		{
			name: "mill build file for scala",
			repo: `
            commits:
              - id: initial-commit
                files:
                  build.sc: |
                      import mill._, mill.scalalib._
                      object example extends ScalaModule {
                        def scalaVersion = "2.13.6"
                      }`,
			expectedBuildTool: millBuildTool,
		},
		{
			name: "general mill build file",
			repo: `
            commits:
              - id: initial-commit
                files:
                  build.mill: |
                    package build
                    import mill.*, javalib.*

                    object foo extends JavaModule {
                        def mvnDeps = Seq(
                            mvn"net.sourceforge.argparse4j:argparse4j:0.9.0",
                            mvn"org.thymeleaf:thymeleaf:3.1.1.RELEASE"
                        )
                    }`,
			expectedBuildTool: millBuildTool,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repo, nil))
			head := must(repo.Repository.Head())
			headCommit := must(repo.Repository.CommitObject(head.Hash()))
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

func TestGitIndexScan(t *testing.T) {
	testCases := []struct {
		name             string
		repoYAML         string
		zipEntries       []*archive.ZipEntry
		expectedCommitID string
		expectedError    bool
	}{
		{
			name: "simple case with direct match",
			repoYAML: `
            commits:
              - id: initial-commit
                files:
                  src/main/java/com/example/App.java: |
                    a`,
			zipEntries: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "com/example/App.java"},
					Body:       []byte("a"),
				},
			},
			expectedCommitID: "initial-commit",
		},
		{
			name: "parent match",
			repoYAML: `
            commits:
              - id: initial-commit
                files:
                  src/main/java/com/example/App.java: |
                    a
              - id: second-commit
                files:
                  src/main/java/com/example/App.java: |
                    b`,
			zipEntries: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "com/example/App.java"},
					Body:       []byte("b"),
				},
			},
			expectedCommitID: "second-commit",
		},
		{
			name: "middle commit match",
			repoYAML: `
            commits:
              - id: initial-commit
                files:
                  src/main/java/com/example/App.java: |
                    a
              - id: second-commit
                files:
                  src/main/java/com/example/App.java: |
                    b
              - id: third-commit
                files:
                  src/main/java/com/example/App.java: |
                    c`,
			zipEntries: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "com/example/App.java"},
					Body:       []byte("b\n"),
				},
			},
			expectedCommitID: "second-commit",
		},
		{
			name: "throw error when no match",
			repoYAML: `
            commits:
              - id: initial-commit
                files:
                  src/main/java/com/example/App.java: |
                    a
              - id: second-commit
                files:
                  src/main/java/com/example/App.java: |
                    b`,
			zipEntries: []*archive.ZipEntry{
				{
					FileHeader: &zip.FileHeader{Name: "com/example/App.java"},
					Body:       []byte("c"),
				},
			},
			expectedCommitID: "",
			expectedError:    true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := must(gitxtest.CreateRepoFromYAML(tc.repoYAML, nil))

			zw := zip.NewWriter(new(bytes.Buffer))
			for _, entry := range tc.zipEntries {
				if err := entry.WriteTo(zw); err != nil {
					t.Fatalf("WriteTo() error: %v", err)
				}
			}
			mockRegistry := &mockMavenRegistry{
				artifactCoordinates: make(map[artifactCoordinates][]byte),
			}
			mockMux := rebuild.RegistryMux{
				Maven: mockRegistry,
			}
			addSourceJarArtifact(mockRegistry, "dummy", "dummy", tc.zipEntries)
			sourceCommit, err := findClosestCommitToSource(context.Background(), rebuild.Target{Package: "dummy", Version: "dummy"}, mockMux, repo.Repository)
			if tc.expectedError {
				if err == nil {
					t.Fatalf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if sourceCommit.Hash != git2.Hash(repo.Commits[tc.expectedCommitID]) {
					t.Errorf("sourceJarGuess() = %v, want %v", sourceCommit.Hash, repo.Commits[tc.expectedCommitID])
				}
			}
		})
	}
}

func addSourceJarArtifact(m *mockMavenRegistry, packageName, version string, entries []*archive.ZipEntry) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		if err := entry.WriteTo(zw); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	m.artifactCoordinates[artifactCoordinates{PackageName: packageName, VersionID: version, FileType: maven.TypeSources}] = buf.Bytes()
}
