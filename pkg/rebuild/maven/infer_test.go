// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
)

// ZipEntry is a helper struct for in-memory zip creation.
type ZipEntry struct {
	Header *zip.FileHeader
	Body   []byte
}

func (z *ZipEntry) WriteTo(zw *zip.Writer) error {
	w, err := zw.CreateHeader(z.Header)
	if err != nil {
		return err
	}
	_, err = w.Write(z.Body)
	return err
}

// mockMavenRegistry is a mock implementation of the maven.Registry interface for testing.
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

func Test_JDKVersionInference(t *testing.T) {
	testCases := []struct {
		name        string
		input       []*ZipEntry
		wantVersion string
	}{
		{
			name: "Manifest declares JDK 17",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					// such a manifest is created by `maven-shade-plugin` which sets `Build-Jdk-Spec` by default.
					[]byte("Manifest-Version: 1.0\r\nBuild-Jdk-Spec: 17.0.2\r\n\r\n"),
				},
				{
					&zip.FileHeader{Name: "com/example/Main.class"},
					[]byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x34, 0x01, 0x02}, // Java 8, but manifest should take precedence
				},
			},
			wantVersion: "17.0.2",
		},
		{
			name: "Infer from bytecode (Java 11)",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					// attribute for JDK version is omitted if `addDefaultEntries` is set to false if running `maven-jar-plugin`
					[]byte("Manifest-Version: 1.0\r\n\r\n"),
				},
				{
					&zip.FileHeader{Name: "com/example/Main.class"},
					[]byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x37, 0x01, 0x02}, // Java 11
				},
			},
			wantVersion: "11",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create in-memory zip (JAR)
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

func Test_getClassFileMajorVersion(t *testing.T) {
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
