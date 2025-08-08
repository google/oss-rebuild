// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/google/oss-rebuild/pkg/registry/maven"
)

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

func Test_getJarJDK(t *testing.T) {
	testCases := []struct {
		name        string
		jarPath     string
		expectedJDK string
	}{
		{
			name:        "Jar with bytecode version 52 (Java 8)",
			jarPath:     "testdata/ldapchai-0.8.7.jar",
			expectedJDK: "8",
		},
		{
			name:        "Jar with MANIFEST declared JDK 11",
			jarPath:     "testdata/shiro-crypto-cipher-1.9.0.jar",
			expectedJDK: "11.0.13",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			jarContent, err := os.ReadFile(tc.jarPath)
			if err != nil {
				t.Fatal(err)
			}
			mockMux := rebuild.RegistryMux{
				Maven: &mockMavenRegistry{
					releaseFileContent: io.NopCloser(bytes.NewReader(jarContent)),
				},
			}

			jdk, err := getJarJDK(context.Background(), tc.jarPath, "", mockMux)
			if err != nil {
				t.Fatalf("getJarJDK() error = %v", err)
			}

			if jdk != tc.expectedJDK {
				t.Errorf("getJarJDK() = %v, want %v", jdk, tc.expectedJDK)
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
