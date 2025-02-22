// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
)

var StableJARBuildMetadata = ZipEntryStabilizer{
	Name: "jar-build-metadata",
	Func: func(zf *MutableZipFile) {
		// Only process MANIFEST.MF files
		if !strings.HasSuffix(zf.Name, "META-INF/MANIFEST.MF") {
			return
		}
		r, err := zf.Open()
		if err != nil {
			return
		}
		manifest, err := ParseManifest(r)
		if err != nil {
			return
		}
		for _, attr := range []string{
			"Archiver-Version",
			"Bnd-LastModified",
			"Build-Jdk",
			"Build-Jdk-Spec",
			"Build-Number",
			"Build-Time",
			"Built-By",
			"Built-Date",
			"Built-Host",
			"Built-OS",
			"Created-By",
			"Hudson-Build-Number",
			"Implementation-Build-Date",
			"Implementation-Build-Java-Vendor",
			"Implementation-Build-Java-Version",
			"Implementation-Build",
			"Jenkins-Build-Number",
			"Originally-Created-By",
			"Os-Version",
			"SCM-Git-Branch",
			"SCM-Revision",
			"SCM-Git-Commit-Dirty",
			"SCM-Git-Commit-ID",
			"SCM-Git-Commit-Abbrev",
			"SCM-Git-Commit-Description",
			"SCM-Git-Commit-Timestamp",
			"Source-Date-Epoch",
		} {
			manifest.MainSection.Delete(attr)
		}
		buf := bytes.NewBuffer(nil)
		if err := WriteManifest(buf, manifest); err != nil {
			return
		}
		zf.SetContent(buf.Bytes())
	},
}

var StableJAROrderOfAttributeValues = ZipEntryStabilizer{
	Name: "jar-attribute-value-order",
	Func: func(zf *MutableZipFile) {
		if !strings.HasSuffix(zf.Name, "META-INF/MANIFEST.MF") {
			return
		}
		r, err := zf.Open()
		if err != nil {
			return
		}
		manifest, err := ParseManifest(r)
		if err != nil {
			return
		}
		// These attributes originate from bnd tool. Full list: https://bnd.bndtools.org/chapters/800-headers.html.
		// Out of these, we only sort the values of the following attributes because we observed them in Reproducible
		// Central dataset. https://github.com/chains-project/reproducible-central/issues/21#issuecomment-2600947048
		for _, attr := range []string{
			"Export-Package",
			"Include-Resource",
			"Provide-Capability",
			"Private-Package",
		} {
			value, _ := manifest.MainSection.Get(attr)
			// Skip empty values
			if value == "" {
				continue
			}
			commaSeparateValues := strings.Split(value, ",")
			// We sort the values to ensure that the order of values is stable
			// Related issues: 1) [fix for Export-Package & Private-Package](https://github.com/bndtools/bnd/issues/5021)
			// 2) [fix for Include-Resource](https://github.com/jvm-repo-rebuild/reproducible-central/issues/99)
			sort.Strings(commaSeparateValues)
			manifest.MainSection.Set(attr, strings.Join(commaSeparateValues, ","))
		}
		buf := bytes.NewBuffer(nil)
		if err := WriteManifest(buf, manifest); err != nil {
			return
		}
		zf.SetContent(buf.Bytes())
	},
}

var StableGitProperties = ZipArchiveStabilizer{
	Name: "jar-git-properties",
	Func: func(mr *MutableZipReader) {
		for _, mf := range mr.File {
			// These files contain git properties set by git-commit-id-maven-plugin.
			// They contain many unreproducible attributes as documented
			// [here](https://github.com/git-commit-id/git-commit-id-maven-plugin/issues/825).

			// Only JSON and properties file formats are available as documented
			// [here](https://github.com/git-commit-id/git-commit-id-maven-plugin/blob/95d616fc7e16018deff3f17e2d03a4b217e55294/src/main/java/pl/project13/maven/git/GitCommitIdMojo.java#L454).
			// By default, these file are created in ${project.build.outputDirectory} and are hence at the root of the jar.
			// We assume that the file name is 'git' as this is the default value for the plugin.
			// However, the plugin allows customizing the file name.
			gitRegex := regexp.MustCompile(`\bgit\.(json|properties)$`)
			if gitRegex.MatchString(mf.Name) {
				mr.DeleteFile(mf.Name)
			}
		}

	},
}
