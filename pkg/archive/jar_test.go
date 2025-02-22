// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"
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
				Stabilizers: []Stabilizer{StableJARBuildMetadata},
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
			test:          "synthetic_example",
			attributeName: []string{"Export-Package"},
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Export-Package: c,\n a,b,d,\n e\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Export-Package: a,b,c,d,e\r\n\r\n"),
				},
			},
		},
		{
			test:          "single_attribute",
			attributeName: []string{"Provide-Capability"},
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Provide-Capability: " +
						"sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scriptEngine=rhino;scriptExtension=ecma;sling.servlet.selectors:List<String>=script," +
						"sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scriptEngine=rhino;scriptExtension=js;sling.servlet.selectors:List<String>=script," +
						"sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scriptEngine=htl;scriptExtension=html," +
						"sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled/templates-access-control\";scriptEngine=htl;scriptExtension=html," +
						"sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled/templates-access-control\";scriptEngine=htl;scriptExtension=html;sling.servlet.selectors:List<String>=\"partials,include\"\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Provide-Capability: " +
						"scriptEngine=htl;scriptExtension=html;sling.servlet;\r\n sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripting/si\r\n ghtly/testing/precompiled\"," +
						"scriptEngine=rhino;scriptExtension=ecma;slin\r\n g.servlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/sc\r\n ripting/sightly/testing/precompiled\";sling.servlet.selectors:List<Strin\r\n g>=script," +
						"scriptEngine=rhino;scriptExtension=js;sling.servlet;sling.ser\r\n vlet.resourceTypes:List<String>=\"org/apache/sling/scripting/sightly/tes\r\n ting/precompiled\";sling.servlet.selectors:List<String>=script," +
						"scriptEng\r\n ine=htl;scriptExtension=html;sling.servlet;sling.servlet.resourceTypes:\r\n List<String>=\"org/apache/sling/scripting/sightly/testing/precompiled/te\r\n mplates-access-control\"," +
						"scriptEngine=htl;scriptExtension=html;sling.ser\r\n vlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripti\r\n ng/sightly/testing/precompiled/templates-access-control\";sling.servlet.\r\n selectors:List<String>=\"partials,include\"\r\n\r\n"),
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
						"Export-Package: org.slf4j.ext;version=\"2.0.6\";uses:=\"org.slf4j\",\n" +
							" org.slf4j.agent;version=\"2.0.6\",\n" +
							" org.slf4j.instrumentation;uses:=javassist;version=\"2.0.6\",\n" +
							" org.slf4j.cal10n;version=\"2.0.6\";uses:=\"ch.qos.cal10n,org.slf4j,org.slf4j.ext\",\n" +
							" org.slf4j.profiler;version=\"2.0.6\";uses:=\"org.slf4j\"\n" +
							"Include-Resource: META-INF/NOTICE=NOTICE,META-INF/LICENSE=LICENSE\n" +
							"Private-Package: org.apache.shiro.util,\n" +
							" org.apache.shiro.ldap,\n" +
							" org.apache.shiro.authc.credential,\n" +
							" org.apache.shiro.authc,\n" +
							" org.apache.shiro.authc.pam,\n" +
							" org.apache.shiro.subject,\n" +
							" org.apache.shiro.subject.support,\n" +
							" org.apache.shiro.dao,\n" +
							" org.apache.shiro,\n" +
							" org.apache.shiro.aop,\n" +
							" org.apache.shiro.env,\n" +
							" org.apache.shiro.mgt,\n" +
							" org.apache.shiro.ini,\n" +
							" org.apache.shiro.jndi,\n" +
							" org.apache.shiro.concurrent,\n" +
							" org.apache.shiro.authz,\n" +
							" org.apache.shiro.authz.annotation,\n" +
							" org.apache.shiro.authz.aop,\n" +
							" org.apache.shiro.authz.permission,\n" +
							" org.apache.shiro.realm,\n" +
							" org.apache.shiro.realm.ldap,\n" +
							" org.apache.shiro.realm.activedirectory,\n" +
							" org.apache.shiro.realm.jdbc,\n" +
							" org.apache.shiro.realm.jndi,\n" +
							" org.apache.shiro.realm.text,\n" +
							" org.apache.shiro.session,\n" +
							" org.apache.shiro.session.mgt,\n" +
							" org.apache.shiro.session.mgt.eis\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte(
						"Export-Package: " +
							"org.slf4j.agent;version=\"2.0.6\"," +
							"org.slf4j.cal10n;uses:=\"\r\n ch.qos.cal10n,org.slf4j," +
							"org.slf4j.ext\";version=\"2.0.6\"," +
							"org.slf4j.ext;us\r\n es:=\"org.slf4j\";version=\"2.0.6\"," +
							"org.slf4j.instrumentation;uses:=javassi\r\n st;version=\"2.0.6\"," +
							"org.slf4j.profiler;uses:=\"org.slf4j\";version=\"2.0.6\"\r\n" +
							"Include-Resource: META-INF/LICENSE=LICENSE,META-INF/NOTICE=NOTICE\r\n" +
							"Private-Package: org.apache.shiro," +
							"org.apache.shiro.aop," +
							"org.apache.shiro.\r\n authc," +
							"org.apache.shiro.authc.credential," +
							"org.apache.shiro.authc.pam," +
							"org.\r\n apache.shiro.authz," +
							"org.apache.shiro.authz.annotation," +
							"org.apache.shiro.a\r\n uthz.aop," +
							"org.apache.shiro.authz.permission," +
							"org.apache.shiro.concurrent,\r\n " +
							"org.apache.shiro.dao," +
							"org.apache.shiro.env," +
							"org.apache.shiro.ini," +
							"org.apac\r\n he.shiro.jndi," +
							"org.apache.shiro.ldap," +
							"org.apache.shiro.mgt," +
							"org.apache.shi\r\n ro.realm," +
							"org.apache.shiro.realm.activedirectory," +
							"org.apache.shiro.realm.\r\n jdbc," +
							"org.apache.shiro.realm.jndi," +
							"org.apache.shiro.realm.ldap," +
							"org.apache\r\n .shiro.realm.text," +
							"org.apache.shiro.session," +
							"org.apache.shiro.session.mgt\r\n ," +
							"org.apache.shiro.session.mgt.eis," +
							"org.apache.shiro.subject," +
							"org.apache.s\r\n hiro.subject.support," +
							"org.apache.shiro.util\r\n\r\n",
					),
				},
			},
		},
		{
			test:          "synthetic_ordering_within_values",
			attributeName: []string{"Export-Package"},
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Export-Package: c2=\"a,b\";a2;b2,a1=\"1234\";c1;b1\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Export-Package: a1=\"1234\";b1;c1,a2;b2;c2=\"a,b\"\r\n\r\n"),
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
				Stabilizers: []Stabilizer{StableJAROrderOfAttributeValues},
			})
			if err != nil {
				t.Fatalf("StabilizeZip(%v) = %v, want nil", tc.test, err)
			}
			// Check output
			zr = must(zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len())))
			for i, ent := range zr.File {
				if ent.Name != tc.expected[i].Name {
					t.Errorf("%v: ZipEntry[%d].Name got %v, want %v", tc.test, i, ent.Name, tc.expected[i].Name)
				} else if diff := cmp.Diff(string(tc.expected[i].Body), string(must(io.ReadAll(must(ent.Open()))))); diff != "" {
					t.Errorf("ZipEntry[%d].Body mismatch (-want +got):\n%s", i, diff)
				}
			}
		})
	}
}

func TestStableGitProperties(t *testing.T) {
	testCases := []struct {
		test     string
		input    []*ZipEntry
		expected []*ZipEntry
	}{
		{
			test: "delete_git_properties",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Built-By: root\r\n\r\n"),
				},
				{
					&zip.FileHeader{Name: "git.properties"},
					[]byte("git.build.user.email=foo@bar.baz\r\ngit.build.user.name=foo bar\r\n\r\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Built-By: root\r\n\r\n"),
				},
			},
		},
		{
			test: "delete_git_json",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Built-By: root\r\n\r\n"),
				},
				{
					&zip.FileHeader{Name: "git.json"},
					[]byte("{\n" +
						"	 \"git.branch\": \"master\",\n" +
						"	 \"git.commit.id.abbrev\": \"e646d22\",\n" +
						"    \"git.commit.id.describe\": \"e646d22\",\n" +
						"    \"git.total.commit.count\": \"1\"" +
						"\n}"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Built-By: root\r\n\r\n"),
				},
			},
		},
		{
			test: "delete_git_json_from_custom_location",
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Built-By: root\r\n\r\n"),
				},
				{
					&zip.FileHeader{Name: "classes/foo"},
					[]byte("bar"),
				},
				{
					&zip.FileHeader{Name: "classes/git.json"},
					[]byte("\n{\n" +
						"    \"git.branch\": \"main\",\n" +
						"    \"git.build.host\": \"ort\",\n" +
						"    \"git.build.user.email\": \"jrivard@gmail.com\",\n" +
						"    \"git.build.user.name\": \"Jason Rivard\",\n" +
						"    \"git.build.version\": \"0.1.0\",\n" +
						"    \"git.closest.tag.commit.count\": \"0\",\n" +
						"    \"git.closest.tag.name\": \"v1_0_1\",\n" +
						"    \"git.commit.author.time\": \"2022-01-06T10:16:03Z\",\n" +
						"    \"git.commit.committer.time\": \"2022-01-06T10:16:03Z\",\n" +
						"    \"git.commit.id\": \"b8b0e095af45ed8b3212b934ce46f2dcb54fdea6\",\n" +
						"    \"git.commit.id.abbrev\": \"b8b0e09\",\n    \"git.commit.id.describe\": \"v1_0_1\",\n" +
						"    \"git.commit.id.describe-short\": \"v1_0_1\",\n" +
						"    \"git.commit.message.full\": \"0.1.0 release\",\n" +
						"    \"git.commit.message.short\": \"0.1.0 release\",\n" +
						"    \"git.commit.time\": \"2022-01-06T10:16:03Z\",\n" +
						"    \"git.commit.user.email\": \"jrivard@gmail.com\",\n" +
						"    \"git.commit.user.name\": \"Jason Rivard\",\n" +
						"    \"git.dirty\": \"false\",\n" +
						"    \"git.local.branch.ahead\": \"5\",\n" +
						"    \"git.local.branch.behind\": \"0\",\n" +
						"    \"git.remote.origin.url\": \"https://github.com/jrivard/chaixml\",\n" +
						"    \"git.tags\": \"v1_0_1\",\n" +
						"    \"git.total.commit.count\": \"8\"\n" +
						"}"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Built-By: root\r\n\r\n"),
				},
				{
					&zip.FileHeader{Name: "classes/foo"},
					[]byte("bar"),
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
				Stabilizers: []any{StableGitProperties},
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
					t.Errorf("Entry %d of %v:\r\ngot:  %+v\r\nwant: %+v", i, tc.test, got[i], tc.expected[i])
				}
			}
		})
	}
}
