// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
)

func TestStableJARBuildMetadata(t *testing.T) {
	testCases := []struct {
		test     string
		input    []*ZipEntry
		expected []*ZipEntry
	}{
		{
			test: "non_manifest_file",
			input: []*ZipEntry{
				{&zip.FileHeader{Name: "src/main/java/App.class"}, []byte("class content")},
			},
			expected: []*ZipEntry{
				{&zip.FileHeader{Name: "src/main/java/App.class"}, []byte("class content")},
			},
		},
		{
			test: "simple_manifest",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Manifest-Version: 1.0\r\nCreated-By: Maven\r\nBuild-Jdk: 11.0.12\r\n\r\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Manifest-Version: 1.0\r\n\r\n"),
				},
			},
		},
		{
			test: "complex_manifest_with_sections",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Manifest-Version: 1.0\r\nCreated-By: Maven\r\nBuild-Jdk: 11.0.12\r\n\r\nName: org/example/\r\nImplementation-Title: Example\r\n\r\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Manifest-Version: 1.0\r\n\r\nName: org/example/\r\nImplementation-Title: Example\r\n\r\n"),
				},
			},
		},
		{
			test: "keep_metadata_in_entries",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Manifest-Version: 1.0\r\n\r\nName: org/example/\r\nCreated-By: Maven\r\n\r\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Manifest-Version: 1.0\r\n\r\nName: org/example/\r\nCreated-By: Maven\r\n\r\n"),
				},
			},
		},
		{
			test: "multiple_files_with_manifest",
			input: []*ZipEntry{
				{&zip.FileHeader{Name: "META-INF/MANIFEST.MF"}, []byte("Manifest-Version: 1.0\r\nBuild-Jdk: 11.0.12\r\nBuild-Time: 2024-01-22\r\n\r\n")},
				{&zip.FileHeader{Name: "com/example/Main.class"}, []byte("class data")},
				{&zip.FileHeader{Name: "META-INF/maven/project.properties"}, []byte("version=1.0.0")},
			},
			expected: []*ZipEntry{
				{&zip.FileHeader{Name: "META-INF/MANIFEST.MF"}, []byte("Manifest-Version: 1.0\r\n\r\n")},
				{&zip.FileHeader{Name: "com/example/Main.class"}, []byte("class data")},
				{&zip.FileHeader{Name: "META-INF/maven/project.properties"}, []byte("version=1.0.0")},
			},
		},
		{
			test: "all_build_metadata_attributes",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte(
						"Manifest-Version: 1.0\r\n" +
							"Archiver-Version: 1.0\r\n" +
							"Bnd-LastModified: 1671890378000\r\n" +
							"Build-Jdk: 11.0.12\r\n" +
							"Build-Jdk-Spec: 11\r\n" +
							"Build-Number: 123\r\n" +
							"Build-Time: 2024-01-22\r\n" +
							"Built-By: jenkins\r\n" +
							"Built-Date: 2024-01-22\r\n" +
							"Built-Host: build-server\r\n" +
							"Built-OS: Linux\r\n" +
							"Created-By: Maven\r\n" +
							"Hudson-Build-Number: 456\r\n" +
							"Implementation-Build-Date: 2024-01-22\r\n" +
							"Implementation-Build-Java-Vendor: Oracle\r\n" +
							"Implementation-Build-Java-Version: 11.0.12\r\n" +
							"Implementation-Build: 789\r\n" +
							"Jenkins-Build-Number: 012\r\n" +
							"Originally-Created-By: Maven\r\n" +
							"Os-Version: Linux 5.15\r\n" +
							"SCM-Git-Branch: main\r\n" +
							"SCM-Revision: abcdef\r\n" +
							"SCM-Git-Commit-Dirty: false\r\n" +
							"SCM-Git-Commit-ID: abcdef123456\r\n" +
							"SCM-Git-Commit-Abbrev: abcdef\r\n" +
							"SCM-Git-Commit-Description: feat: new feature\r\n" +
							"SCM-Git-Commit-Timestamp: 1671890378\r\n" +
							"Source-Date-Epoch: 1671890378\r\n" +
							"Implementation-Title: Test Project\r\n" +
							"Implementation-Version: 1.0.0\r\n\r\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Manifest-Version: 1.0\r\nImplementation-Title: Test Project\r\nImplementation-Version: 1.0.0\r\n\r\n"),
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			// Create input zip
			var input bytes.Buffer
			{
				zw := zip.NewWriter(&input)
				for _, entry := range tc.input {
					orDie(entry.WriteTo(zw))
				}
				orDie(zw.Close())
			}

			// Process with stabilizer
			var output bytes.Buffer
			zr := must(zip.NewReader(bytes.NewReader(input.Bytes()), int64(input.Len())))
			err := StabilizeZip(zr, zip.NewWriter(&output), StabilizeOpts{
				Stabilizers: []any{StableJARBuildMetadata},
			})
			if err != nil {
				t.Fatalf("StabilizeZip(%v) = %v, want nil", tc.test, err)
			}

			// Check output
			var got []ZipEntry
			{
				zr := must(zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len())))
				for _, ent := range zr.File {
					got = append(got, ZipEntry{&ent.FileHeader, must(io.ReadAll(must(ent.Open())))})
				}
			}

			if len(got) != len(tc.expected) {
				t.Fatalf("StabilizeZip(%v) got %v entries, want %v", tc.test, len(got), len(tc.expected))
			}

			for i := range got {
				if !all(
					got[i].FileHeader.Name == tc.expected[i].FileHeader.Name,
					bytes.Equal(got[i].Body, tc.expected[i].Body),
				) {
					t.Errorf("Entry %d of %v:\r\ngot:  %+v\r\nwant: %+v", i, tc.test, string(got[i].Body), string(tc.expected[i].Body))
				}
			}
		})
	}
}
