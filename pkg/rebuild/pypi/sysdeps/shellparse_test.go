// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package sysdeps

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParsePackageManagerCommands(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		provenance string
		want       []DependencyIdentifier
	}{
		{
			name:       "YumInstallBasicAndFlagsBeforeSubcommand",
			script:     "yum -y --enablerepo=epel install graphviz-devel zlib-devel",
			provenance: "cibw",
			want: []DependencyIdentifier{
				{Namespace: NamespaceYum, Name: "graphviz-devel", Provenance: "cibw"},
				{Namespace: NamespaceYum, Name: "zlib-devel", Provenance: "cibw"},
			},
		},
		{
			name: "DnfChainedWithLineContinuationsAndBuildSteps",
			script: `dnf install -y gcc gcc-c++ \
				expat-devel zlib-devel \
				gd-devel cairo-devel pango-devel && \
				curl -L https://example.com/src.tar.gz -o /tmp/src.tar.gz && \
				make install`,
			provenance: "release.yml",
			want: []DependencyIdentifier{
				{Namespace: NamespaceDnf, Name: "gcc", Provenance: "release.yml"},
				{Namespace: NamespaceDnf, Name: "gcc-c++", Provenance: "release.yml"},
				{Namespace: NamespaceDnf, Name: "expat-devel", Provenance: "release.yml"},
				{Namespace: NamespaceDnf, Name: "zlib-devel", Provenance: "release.yml"},
				{Namespace: NamespaceDnf, Name: "gd-devel", Provenance: "release.yml"},
				{Namespace: NamespaceDnf, Name: "cairo-devel", Provenance: "release.yml"},
				{Namespace: NamespaceDnf, Name: "pango-devel", Provenance: "release.yml"},
			},
		},
		{
			name:       "ApkAddWithVirtualFlagAndVersionPin",
			script:     "apk --no-cache add --virtual .build-deps graphviz-dev=14.1.5-r0 cairo-dev@edge",
			provenance: "cibw",
			want: []DependencyIdentifier{
				{Namespace: NamespaceApk, Name: "graphviz-dev", Provenance: "cibw"},
				{Namespace: NamespaceApk, Name: "cairo-dev", Provenance: "cibw"},
			},
		},
		{
			name:       "AptGetWithSudoAndEnvAssignments",
			script:     "sudo apt-get update && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends graphviz graphviz-dev=2.42.2-7build2",
			provenance: "test.yml",
			want: []DependencyIdentifier{
				{Namespace: NamespaceApt, Name: "graphviz", Provenance: "test.yml"},
				{Namespace: NamespaceApt, Name: "graphviz-dev", Provenance: "test.yml"},
			},
		},
		{
			name:       "SkipsVariablesLocalFilesAndComments",
			script:     "yum install -y $EXTRA_PKGS /tmp/custom.rpm ./local.rpm valid-devel # comment with yum install fake-pkg",
			provenance: "cibw",
			want: []DependencyIdentifier{
				{Namespace: NamespaceYum, Name: "valid-devel", Provenance: "cibw"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParsePackageManagerCommands(tc.script, tc.provenance)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParsePackageManagerCommands() diff (-want +got):\n%s", diff)
			}
		})
	}
}
