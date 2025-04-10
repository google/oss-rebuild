// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"bytes"
	"path"
	"sort"
	"strings"
)

var AllJarStabilizers = []Stabilizer{
	StableJARBuildMetadata,
	StableJAROrderOfAttributeValues,
	StableGitProperties,
}

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
			"Build-Date",
			"Build-Date-UTC",
			"Build-Host",
			"Build-Id",
			"Build-Java-Version",
			"Build-Jdk",
			"Build-Jdk-Spec",
			"Build-Job",
			"Build-Number",
			"Build-OS",
			"Build-Status",
			"Build-Time",
			"Build-Timestamp",
			"Build-Tool",
			"Build-Url",
			"Built-By",
			"Built-Date",
			"Built-Host",
			"Built-JDK",
			"Built-On",
			"Built-OS",
			"Built-Status",
			"Created-By",
			"DSTAMP",
			"Eclipse-SourceReferences",
			"Git-Commit-Id-Describe",
			"Git-Remote-Origin-Url",
			"Git-SHA",
			"Git-Descriptor",
			"git-describe",
			"git-tags",
			"hash",
			"Hudson-Build-Number",
			"Implementation-Build-Date",
			"Implementation-Build-Java-Vendor",
			"Implementation-Build-Java-Version",
			"Implementation-Build",
			"Ion-Java-Build-Time",
			"Java-Vendor",
			"Java-Version",
			"JCabi-Date",
			"Jenkins-Build-Number",
			"Maven-Version",
			"Module-Origin",
			"Originally-Created-By",
			"Os-Arch",
			"Os-Name",
			"Os-Version",
			"SCM-Git-Branch",
			"SCM-Git-Commit-Dirty",
			"SCM-Git-Commit-ID",
			"SCM-Git-Commit-Abbrev",
			"SCM-Git-Commit-Description",
			"SCM-Git-Commit-Timestamp",
			"SCM-Revision",
			"SHA-256-Digest",
			"Source-Date-Epoch",
			"Sunset-BuiltOn",
			"TODAY",
			"Tool",
			"TSTAMP",
			"url",
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
			subvalues := splitPreservingQuotes(value, ',')
			// We sort the values to ensure that the order of values is stable
			// Related issues: 1) [fix for Export-Package & Private-Package](https://github.com/bndtools/bnd/issues/5021)
			// 2) [fix for Include-Resource](https://github.com/jvm-repo-rebuild/reproducible-central/issues/99)
			sort.Strings(subvalues)
			for i, subvalue := range subvalues {
				subvalueArray := splitPreservingQuotes(subvalue, ';')
				sort.Strings(subvalueArray)
				subvalues[i] = strings.Join(subvalueArray, ";")
			}
			manifest.MainSection.Set(attr, strings.Join(subvalues, ","))
		}
		buf := bytes.NewBuffer(nil)
		if err := WriteManifest(buf, manifest); err != nil {
			return
		}
		zf.SetContent(buf.Bytes())
	},
}

// splitPreservingQuotes splits a string by a separator while preserving quoted sections
func splitPreservingQuotes(s string, sep rune) []string {
	var result []string
	var current strings.Builder
	inQuote := false
	for _, char := range s {
		switch char {
		case '"':
			inQuote = !inQuote
			current.WriteRune(char)
		case sep:
			if inQuote {
				current.WriteRune(char)
			} else {
				result = append(result, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}
	// Add the last segment
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

var StableGitProperties = ZipEntryStabilizer{
	Name: "jar-git-properties",
	Func: func(zf *MutableZipFile) {
		// These files contain git properties set by git-commit-id-maven-plugin.
		// They contain many unreproducible attributes as documented
		// [here](https://github.com/git-commit-id/git-commit-id-maven-plugin/issues/825).
		// Only JSON and properties file formats are available as documented
		// [here](https://github.com/git-commit-id/git-commit-id-maven-plugin/blob/95d616fc7e16018deff3f17e2d03a4b217e55294/src/main/java/pl/project13/maven/git/GitCommitIdMojo.java#L454).
		// By default, these file are created in ${project.build.outputDirectory} and are hence at the root of the jar.
		// We assume that the file name is 'git' as this is the default value for the plugin.
		// We don't handle the case where the file name is changed by the user.
		if path.Base(zf.Name) == "git.json" {
			zf.SetContent([]byte("{}"))
		}

		if path.Base(zf.Name) == "git.properties" {
			zf.SetContent([]byte{})
		}
	},
}
