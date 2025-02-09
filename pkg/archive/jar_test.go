// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
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

func TestStableOrderOfAttributeValues(t *testing.T) {
	testCases := []struct {
		test          string
		attributeName []string
		input         []*ZipEntry
		expected      []*ZipEntry
	}{
		{
			test:          "single_attribute",
			attributeName: []string{"Provide-Capability"},
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Provide-Capability: sling.servlet;sling.servlet.resourceTypes:List<Strin\n g>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scriptEngin\n e=rhino;scriptExtension=ecma;sling.servlet.selectors:List<String>=scrip\n t,sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sl\n ing/scripting/sightly/testing/precompiled\";scriptEngine=rhino;scriptExt\n ension=js;sling.servlet.selectors:List<String>=script,sling.servlet;sli\n ng.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sight\n ly/testing/precompiled\";scriptEngine=htl;scriptExtension=html,sling.ser\n vlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripti\n ng/sightly/testing/precompiled/templates-access-control\";scriptEngine=h\n tl;scriptExtension=html,sling.servlet;sling.servlet.resourceTypes:List<\n String>=\"org/apache/sling/scripting/sightly/testing/precompiled/templat\n es-access-control\";scriptEngine=htl;scriptExtension=html;sling.servlet.\n selectors:List<String>=\"partials,include\"\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Provide-Capability: include\",sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scriptEngine=htl;scriptExtension=html,sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scriptEngine=rhino;scriptExtension=ecma;sling.servlet.selectors:List<String>=script,sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scriptEngine=rhino;scriptExtension=js;sling.servlet.selectors:List<String>=script,sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled/templates-access-control\";scriptEngine=htl;scriptExtension=html,sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled/templates-access-control\";scriptEngine=htl;scriptExtension=html;sling.servlet.selectors:List<String>=\"partials\n"),
				},
			},
		},
		{
			test:          "multiple_attributes",
			attributeName: []string{"Export-Package", "Include-Resource"},
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte(
						"Export-Package: org.slf4j.ext;version=\"2.0.6\";uses:=\"org.slf4j\",org.slf4\n j.agent;version=\"2.0.6\",org.slf4j.instrumentation;uses:=javassist;versi\n on=\"2.0.6\",org.slf4j.cal10n;version=\"2.0.6\";uses:=\"ch.qos.cal10n,org.sl\n f4j,org.slf4j.ext\",org.slf4j.profiler;version=\"2.0.6\";uses:=\"org.slf4j\"\n" +
							"Include-Resource: META-INF/NOTICE=NOTICE,META-INF/LICENSE=LICENSE\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte(
						"Export-Package: org.slf4j,org.slf4j.agent;version=\"2.0.6\",org.slf4j.cal10n;version=\"2.0.6\";uses:=\"ch.qos.cal10n,org.slf4j.ext\",org.slf4j.ext;version=\"2.0.6\";uses:=\"org.slf4j\",org.slf4j.instrumentation;uses:=javassist;version=\"2.0.6\",org.slf4j.profiler;version=\"2.0.6\";uses:=\"org.slf4j\"\n" +
							"Include-Resource: META-INF/LICENSE=LICENSE,META-INF/NOTICE=NOTICE\n"),
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
				Stabilizers: []any{StableJAROrderOfAttributeValues},
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

			if got[0].Name != tc.expected[0].Name {
				t.Errorf("StabilizeZip(%v) got %v, want %v", tc.test, got[0].Name, tc.expected[0].Name)
			}

			manifestGot, err := ParseManifest(bytes.NewReader(got[0].Body))
			if err != nil {
				t.Fatalf("Could not parse actual manifest: %v", err)
			}
			manifestWant, err := ParseManifest(bytes.NewReader(tc.expected[0].Body))
			if err != nil {
				t.Fatalf("Could not parse expected manifest: %v", err)
			}
			for _, attr := range tc.attributeName {
				gotOrder := getSeparatedValues(manifestGot.MainSection.Attributes[attr])
				wantOrder := getSeparatedValues(manifestWant.MainSection.Attributes[attr])
				if gotOrder == nil || wantOrder == nil {
					t.Fatalf("Could not parse expected or actual manifest")
				}

				if len(gotOrder) != len(wantOrder) {
					t.Fatalf("StabilizeZip(%v) got %v entries, want %v", tc.test, len(gotOrder), len(wantOrder))
				}
				for i := range gotOrder {
					if gotOrder[i] != wantOrder[i] {
						t.Errorf("Entry %d of %v:\r\ngot:  %+v\r\nwant: %+v", i, tc.test, gotOrder[i], wantOrder[i])
					}
				}
			}
		})
	}
}

func getSeparatedValues(attributeValue string) []string {
	value := strings.ReplaceAll(attributeValue, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	value = strings.ReplaceAll(value, " ", "")
	commaSeparateValues := strings.Split(value, ",")
	return commaSeparateValues
}
