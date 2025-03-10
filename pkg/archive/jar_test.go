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
						"sling.servlet;sling.servlet.resourceTypes:List<Strin\r\n g>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scriptEngin\r\n e=htl;scriptExtension=html," +
						"sling.servlet;sling.servlet.resourceTypes:Li\r\n st<String>=\"org/apache/sling/scripting/sightly/testing/precompiled\";scr\r\n iptEngine=rhino;scriptExtension=ecma;sling.servlet.selectors:List<Strin\r\n g>=script," +
						"sling.servlet;sling.servlet.resourceTypes:List<String>=\"org/a\r\n pache/sling/scripting/sightly/testing/precompiled\";scriptEngine=rhino;s\r\n criptExtension=js;sling.servlet.selectors:List<String>=script," +
						"sling.ser\r\n vlet;sling.servlet.resourceTypes:List<String>=\"org/apache/sling/scripti\r\n ng/sightly/testing/precompiled/templates-access-control\";scriptEngine=h\r\n tl;scriptExtension=html," +
						"sling.servlet;sling.servlet.resourceTypes:List<\r\n String>=\"org/apache/sling/scripting/sightly/testing/precompiled/templat\r\n es-access-control\";scriptEngine=htl;scriptExtension=html;sling.servlet.\r\n selectors:List<String>=\"partials,include\"\r\n\r\n"),
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
						"Export-Package: org.slf4j.agent;version=\"2.0.6\"," +
							"org.slf4j.cal10n;version\r\n =\"2.0.6\";uses:=\"ch.qos.cal10n,org.slf4j,org.slf4j.ext\"," +
							"org.slf4j.ext;ve\r\n rsion=\"2.0.6\";uses:=\"org.slf4j\"," +
							"org.slf4j.instrumentation;uses:=javassi\r\n st;version=\"2.0.6\"," +
							"org.slf4j.profiler;version=\"2.0.6\";uses:=\"org.slf4j\"\r\n" +
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
					[]byte("Export-Package: c2;a2;b2,a1;c1;b1\n"),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Export-Package: a1;b1;c1,a2;b2;c2\r\n\r\n"),
				},
			},
		},
		{
			test:          "ordering_within_values_logback-core-1.2.12",
			attributeName: []string{"Export-Package"},
			input: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Export-Package: ch.qos.logback.core;version=\"1.2.12\";uses:=\"ch.qos.log\n back.core.encoder,ch.qos.logback.core.filter,ch.qos.logback.core.help\n ers,ch.qos.logback.core.joran.spi,ch.qos.logback.core.spi,ch.qos.logb\n ack.core.status,ch.qos.logback.core.util\",ch.qos.logback.core.boolex;\n version=\"1.2.12\";uses:=\"ch.qos.logback.core.spi\",ch.qos.logback.core.\n encoder;version=\"1.2.12\";uses:=\"ch.qos.logback.core,ch.qos.logback.co\n re.spi\",ch.qos.logback.core.filter;version=\"1.2.12\";uses:=\"ch.qos.log\n back.core.boolex,ch.qos.logback.core.spi\",ch.qos.logback.core.helpers\n ;version=\"1.2.12\";uses:=\"ch.qos.logback.core\",ch.qos.logback.core.hoo\n k;version=\"1.2.12\";uses:=\"ch.qos.logback.core.spi,ch.qos.logback.core\n .util\",ch.qos.logback.core.html;version=\"1.2.12\";uses:=\"ch.qos.logbac\n k.core,ch.qos.logback.core.pattern\",ch.qos.logback.core.joran;version\n =\"1.2.12\";uses:=\"ch.qos.logback.core,ch.qos.logback.core.joran.event,\n ch.qos.logback.core.joran.spi,ch.qos.logback.core.joran.util.beans,ch\n .qos.logback.core.spi,org.xml.sax\",ch.qos.logback.core.joran.action;v\n ersion=\"1.2.12\";uses:=\"ch.qos.logback.core.joran.spi,ch.qos.logback.c\n ore.joran.util,ch.qos.logback.core.joran.util.beans,ch.qos.logback.co\n re.spi,ch.qos.logback.core.util,org.xml.sax\",ch.qos.logback.core.jora\n n.conditional;version=\"1.2.12\";uses:=\"ch.qos.logback.core.joran.actio\n n,ch.qos.logback.core.joran.event,ch.qos.logback.core.joran.spi,ch.qo\n s.logback.core.spi,org.codehaus.commons.compiler,org.xml.sax\",ch.qos.\n logback.core.joran.event;version=\"1.2.12\";uses:=\"ch.qos.logback.core,\n ch.qos.logback.core.joran.spi,ch.qos.logback.core.spi,ch.qos.logback.\n core.status,org.xml.sax,org.xml.sax.helpers\",ch.qos.logback.core.jora\n n.event.stax;version=\"1.2.12\";uses:=\"ch.qos.logback.core,ch.qos.logba\n ck.core.joran.spi,ch.qos.logback.core.spi,javax.xml.stream,javax.xml.\n stream.events\",ch.qos.logback.core.joran.node;version=\"1.2.12\",ch.qos\n .logback.core.joran.spi;version=\"1.2.12\";uses:=\"ch.qos.logback.core,c\n h.qos.logback.core.joran.action,ch.qos.logback.core.joran.event,ch.qo\n s.logback.core.spi,ch.qos.logback.core.status,org.xml.sax\",ch.qos.log\n back.core.joran.util;version=\"1.2.12\";uses:=\"ch.qos.logback.core,ch.q\n os.logback.core.joran.spi,ch.qos.logback.core.joran.util.beans,ch.qos\n .logback.core.spi,ch.qos.logback.core.util\",ch.qos.logback.core.joran\n .util.beans;version=\"1.2.12\";uses:=\"ch.qos.logback.core,ch.qos.logbac\n k.core.spi\",ch.qos.logback.core.layout;version=\"1.2.12\";uses:=\"ch.qos\n .logback.core\",ch.qos.logback.core.net;version=\"1.2.12\";uses:=\"ch.qos\n .logback.core,ch.qos.logback.core.boolex,ch.qos.logback.core.helpers,\n ch.qos.logback.core.net.ssl,ch.qos.logback.core.pattern,ch.qos.logbac\n k.core.sift,ch.qos.logback.core.spi,ch.qos.logback.core.util,javax.ma\n il,javax.net\",ch.qos.logback.core.net.server;version=\"1.2.12\";uses:=\"\n ch.qos.logback.core,ch.qos.logback.core.net.ssl,ch.qos.logback.core.s\n pi,javax.net\",ch.qos.logback.core.net.ssl;version=\"1.2.12\";uses:=\"ch.\n qos.logback.core.joran.spi,ch.qos.logback.core.spi,javax.net,javax.ne\n t.ssl\",ch.qos.logback.core.pattern;version=\"1.2.12\";uses:=\"ch.qos.log\n back.core,ch.qos.logback.core.encoder,ch.qos.logback.core.spi,ch.qos.\n logback.core.status\",ch.qos.logback.core.pattern.color;version=\"1.2.1\n 2\";uses:=\"ch.qos.logback.core.pattern\",ch.qos.logback.core.pattern.pa\n rser;version=\"1.2.12\";uses:=\"ch.qos.logback.core.pattern,ch.qos.logba\n ck.core.pattern.util,ch.qos.logback.core.spi\",ch.qos.logback.core.pat\n tern.util;version=\"1.2.12\",ch.qos.logback.core.property;version=\"1.2.\n 12\";uses:=\"ch.qos.logback.core\",ch.qos.logback.core.read;version=\"1.2\n .12\";uses:=\"ch.qos.logback.core\",ch.qos.logback.core.recovery;version\n =\"1.2.12\";uses:=\"ch.qos.logback.core,ch.qos.logback.core.status\",ch.q\n os.logback.core.rolling;version=\"1.2.12\";uses:=\"ch.qos.logback.core,c\n h.qos.logback.core.joran.spi,ch.qos.logback.core.rolling.helper,ch.qo\n s.logback.core.spi,ch.qos.logback.core.util\",ch.qos.logback.core.roll\n ing.helper;version=\"1.2.12\";uses:=\"ch.qos.logback.core,ch.qos.logback\n .core.pattern,ch.qos.logback.core.rolling,ch.qos.logback.core.spi\",ch\n .qos.logback.core.sift;version=\"1.2.12\";uses:=\"ch.qos.logback.core,ch\n .qos.logback.core.joran,ch.qos.logback.core.joran.event,ch.qos.logbac\n k.core.joran.spi,ch.qos.logback.core.spi,ch.qos.logback.core.util\",ch\n .qos.logback.core.spi;version=\"1.2.12\";uses:=\"ch.qos.logback.core,ch.\n qos.logback.core.filter,ch.qos.logback.core.helpers,ch.qos.logback.co\n re.status\",ch.qos.logback.core.status;version=\"1.2.12\";uses:=\"ch.qos.\n logback.core,ch.qos.logback.core.spi,javax.servlet,javax.servlet.http\n \",ch.qos.logback.core.subst;version=\"1.2.12\";uses:=\"ch.qos.logback.co\n re.spi\",ch.qos.logback.core.util;version=\"1.2.12\";uses:=\"ch.qos.logba\n ck.core,ch.qos.logback.core.rolling,ch.qos.logback.core.rolling.helpe\n r,ch.qos.logback.core.spi,ch.qos.logback.core.status,javax.naming\""),
				},
			},
			expected: []*ZipEntry{
				{
					&zip.FileHeader{Name: "META-INF/MANIFEST.MF"},
					[]byte("Export-Package: ch.qos.logback.core.boolex;uses:=\"ch.qos.logback.core.sp\r\n i\";version=\"1.2.12\",ch.qos.logback.core.encoder;uses:=\"ch.qos.logback.c\r\n ore,ch.qos.logback.core.spi\";version=\"1.2.12\",ch.qos.logback.core.filte\r\n r;uses:=\"ch.qos.logback.core.boolex,ch.qos.logback.core.spi\";version=\"1\r\n .2.12\",ch.qos.logback.core.helpers;uses:=\"ch.qos.logback.core\";version=\r\n \"1.2.12\",ch.qos.logback.core.hook;uses:=\"ch.qos.logback.core.spi,ch.qos\r\n .logback.core.util\";version=\"1.2.12\",ch.qos.logback.core.html;uses:=\"ch\r\n .qos.logback.core,ch.qos.logback.core.pattern\";version=\"1.2.12\",ch.qos.\r\n logback.core.joran.action;uses:=\"ch.qos.logback.core.joran.spi,ch.qos.l\r\n ogback.core.joran.util,ch.qos.logback.core.joran.util.beans,ch.qos.logb\r\n ack.core.spi,ch.qos.logback.core.util,org.xml.sax\";version=\"1.2.12\",ch.\r\n qos.logback.core.joran.conditional;uses:=\"ch.qos.logback.core.joran.act\r\n ion,ch.qos.logback.core.joran.event,ch.qos.logback.core.joran.spi,ch.qo\r\n s.logback.core.spi,org.codehaus.commons.compiler,org.xml.sax\";version=\"\r\n 1.2.12\",ch.qos.logback.core.joran.event.stax;uses:=\"ch.qos.logback.core\r\n ,ch.qos.logback.core.joran.spi,ch.qos.logback.core.spi,javax.xml.stream\r\n ,javax.xml.stream.events\";version=\"1.2.12\",ch.qos.logback.core.joran.ev\r\n ent;uses:=\"ch.qos.logback.core,ch.qos.logback.core.joran.spi,ch.qos.log\r\n back.core.spi,ch.qos.logback.core.status,org.xml.sax,org.xml.sax.helper\r\n s\";version=\"1.2.12\",ch.qos.logback.core.joran.node;version=\"1.2.12\",ch.\r\n qos.logback.core.joran.spi;uses:=\"ch.qos.logback.core,ch.qos.logback.co\r\n re.joran.action,ch.qos.logback.core.joran.event,ch.qos.logback.core.spi\r\n ,ch.qos.logback.core.status,org.xml.sax\";version=\"1.2.12\",ch.qos.logbac\r\n k.core.joran.util.beans;uses:=\"ch.qos.logback.core,ch.qos.logback.core.\r\n spi\";version=\"1.2.12\",ch.qos.logback.core.joran.util;uses:=\"ch.qos.logb\r\n ack.core,ch.qos.logback.core.joran.spi,ch.qos.logback.core.joran.util.b\r\n eans,ch.qos.logback.core.spi,ch.qos.logback.core.util\";version=\"1.2.12\"\r\n ,ch.qos.logback.core.joran;uses:=\"ch.qos.logback.core,ch.qos.logback.co\r\n re.joran.event,ch.qos.logback.core.joran.spi,ch.qos.logback.core.joran.\r\n util.beans,ch.qos.logback.core.spi,org.xml.sax\";version=\"1.2.12\",ch.qos\r\n .logback.core.layout;uses:=\"ch.qos.logback.core\";version=\"1.2.12\",ch.qo\r\n s.logback.core.net.server;uses:=\"ch.qos.logback.core,ch.qos.logback.cor\r\n e.net.ssl,ch.qos.logback.core.spi,javax.net\";version=\"1.2.12\",ch.qos.lo\r\n gback.core.net.ssl;uses:=\"ch.qos.logback.core.joran.spi,ch.qos.logback.\r\n core.spi,javax.net,javax.net.ssl\";version=\"1.2.12\",ch.qos.logback.core.\r\n net;uses:=\"ch.qos.logback.core,ch.qos.logback.core.boolex,ch.qos.logbac\r\n k.core.helpers,ch.qos.logback.core.net.ssl,ch.qos.logback.core.pattern,\r\n ch.qos.logback.core.sift,ch.qos.logback.core.spi,ch.qos.logback.core.ut\r\n il,javax.mail,javax.net\";version=\"1.2.12\",ch.qos.logback.core.pattern.c\r\n olor;uses:=\"ch.qos.logback.core.pattern\";version=\"1.2.12\",ch.qos.logbac\r\n k.core.pattern.parser;uses:=\"ch.qos.logback.core.pattern,ch.qos.logback\r\n .core.pattern.util,ch.qos.logback.core.spi\";version=\"1.2.12\",ch.qos.log\r\n back.core.pattern.util;version=\"1.2.12\",ch.qos.logback.core.pattern;use\r\n s:=\"ch.qos.logback.core,ch.qos.logback.core.encoder,ch.qos.logback.core\r\n .spi,ch.qos.logback.core.status\";version=\"1.2.12\",ch.qos.logback.core.p\r\n roperty;uses:=\"ch.qos.logback.core\";version=\"1.2.12\",ch.qos.logback.cor\r\n e.read;uses:=\"ch.qos.logback.core\";version=\"1.2.12\",ch.qos.logback.core\r\n .recovery;uses:=\"ch.qos.logback.core,ch.qos.logback.core.status\";versio\r\n n=\"1.2.12\",ch.qos.logback.core.rolling.helper;uses:=\"ch.qos.logback.cor\r\n e,ch.qos.logback.core.pattern,ch.qos.logback.core.rolling,ch.qos.logbac\r\n k.core.spi\";version=\"1.2.12\",ch.qos.logback.core.rolling;uses:=\"ch.qos.\r\n logback.core,ch.qos.logback.core.joran.spi,ch.qos.logback.core.rolling.\r\n helper,ch.qos.logback.core.spi,ch.qos.logback.core.util\";version=\"1.2.1\r\n 2\",ch.qos.logback.core.sift;uses:=\"ch.qos.logback.core,ch.qos.logback.c\r\n ore.joran,ch.qos.logback.core.joran.event,ch.qos.logback.core.joran.spi\r\n ,ch.qos.logback.core.spi,ch.qos.logback.core.util\";version=\"1.2.12\",ch.\r\n qos.logback.core.spi;uses:=\"ch.qos.logback.core,ch.qos.logback.core.fil\r\n ter,ch.qos.logback.core.helpers,ch.qos.logback.core.status\";version=\"1.\r\n 2.12\",ch.qos.logback.core.status;uses:=\"ch.qos.logback.core,ch.qos.logb\r\n ack.core.spi,javax.servlet,javax.servlet.http\";version=\"1.2.12\",ch.qos.\r\n logback.core.subst;uses:=\"ch.qos.logback.core.spi\";version=\"1.2.12\",ch.\r\n qos.logback.core.util;uses:=\"ch.qos.logback.core,ch.qos.logback.core.ro\r\n lling,ch.qos.logback.core.rolling.helper,ch.qos.logback.core.spi,ch.qos\r\n .logback.core.status,javax.naming\";version=\"1.2.12\",ch.qos.logback.core\r\n ;uses:=\"ch.qos.logback.core.encoder,ch.qos.logback.core.filter,ch.qos.l\r\n ogback.core.helpers,ch.qos.logback.core.joran.spi,ch.qos.logback.core.s\r\n pi,ch.qos.logback.core.status,ch.qos.logback.core.util\";version=\"1.2.12\r\n \"\r\n\r\n"),
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
