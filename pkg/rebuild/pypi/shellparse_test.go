// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package pypi

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi/platform"
)

func TestParsePackageManagerCommands(t *testing.T) {
	tests := []struct {
		name   string
		script string
		libc   platform.LibcImpl
		want   []string
	}{
		{
			name:   "YumInstallBasicAndFlagsBeforeSubcommand",
			script: "yum -y --enablerepo=epel install graphviz-devel zlib-devel",
			libc:   platform.Glibc,
			want:   []string{"graphviz-devel", "zlib-devel"},
		},
		{
			name: "DnfChainedWithLineContinuationsAndBuildSteps",
			script: `dnf install -y gcc gcc-c++ \
				expat-devel zlib-devel \
				gd-devel cairo-devel pango-devel && \
				curl -L https://example.com/src.tar.gz -o /tmp/src.tar.gz && \
				make install`,
			libc: platform.Glibc,
			want: []string{
				"gcc",
				"gcc-c++",
				"expat-devel",
				"zlib-devel",
				"gd-devel",
				"cairo-devel",
				"pango-devel",
			},
		},
		{
			name:   "ApkAddWithVirtualFlagAndVersionPin",
			script: "apk --no-cache add --virtual .build-deps graphviz-dev=14.1.5-r0 'cairo-dev>=1.18' pango-dev@edge",
			libc:   platform.Musl,
			want:   []string{"graphviz-dev", "cairo-dev", "pango-dev"},
		},
		{
			name:   "FiltersByTargetLibc",
			script: "yum install -y graphviz-devel || apk add --no-cache graphviz-dev",
			libc:   platform.Musl,
			want:   []string{"graphviz-dev"},
		},
		{
			name:   "InlineIfThenElifConditional",
			script: "if command -v yum; then yum install -y foo-devel; elif command -v apk; then apk add foo-dev; fi",
			libc:   platform.Glibc,
			want:   []string{"foo-devel"},
		},
		{
			name:   "BraceGroupNegationNiceAndBackground",
			script: "{ ! nice -n 10 yum install -y brace-devel; } && yum install -y bg-devel & wait",
			libc:   platform.Glibc,
			want:   []string{"brace-devel", "bg-devel"},
		},
		{
			name:   "SkipsVariablesLocalFilesAndComments",
			script: "yum install -y $EXTRA_PKGS /tmp/custom.rpm ./local.rpm valid-devel # comment with yum install fake-pkg",
			libc:   platform.Glibc,
			want:   []string{"valid-devel"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePackageManagerCommands(tc.script, tc.libc)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("parsePackageManagerCommands() diff (-want +got):\n%s", diff)
			}
		})
	}
}
