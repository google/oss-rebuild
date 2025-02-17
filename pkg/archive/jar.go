// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"bytes"
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
			// Related issues: https: //issues.apache.org/jira/browse/FELIX-6496
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
