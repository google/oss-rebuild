// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package parsing

import (
	"context"
	"testing"
)

func TestNeedsSetupCfgSanitize(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dir      string
		version  string
		repoYAML string
		want     bool
	}{
		{
			name:    "TagBuildDevOnFinalRelease",
			version: "2.0.52",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.cfg: |
        [metadata]
        name = SQLAlchemy
        version = attr: sqlalchemy.__version__
        [egg_info]
        tag_build = dev
`,
			want: true,
		},
		{
			name:    "TagBuildDotDevZeroOnFinalRelease",
			version: "1.4.0",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.cfg: |
        [egg_info]
        tag_build = .dev0
`,
			want: true,
		},
		{
			name:    "TagBuildMatchesPreReleaseVersion",
			version: "2.0.52.dev0",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.cfg: |
        [egg_info]
        tag_build = dev
`,
			want: false,
		},
		{
			name:    "EmptyTagBuildAndZeroTagDate",
			version: "1.0.0",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.cfg: |
        [egg_info]
        tag_build =
        tag_date = 0
`,
			want: false,
		},
		{
			name:    "TagDateEnabledOnFinalRelease",
			version: "1.0.0",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.cfg: |
        [egg_info]
        tag_date = 1
`,
			want: true,
		},
		{
			// A dated release cannot be reproduced by clearing tag_date (which
			// drops the date) or keeping it (which stamps today's date), so
			// setup.cfg is left untouched.
			name:    "TagDateMatchesDatedRelease",
			version: "1.0.0.post20261009",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.cfg: |
        [egg_info]
        tag_date = true
`,
			want: false,
		},
		{
			name:    "NoSetupCfg",
			version: "1.0.0",
			repoYAML: `
commits:
  - id: c1
    files:
      pyproject.toml: |
        [project]
        name = "foo"
        version = "1.0.0"
`,
			want: false,
		},
		{
			name:    "SubdirectorySetupCfg",
			dir:     "sub",
			version: "1.0.0",
			repoYAML: `
commits:
  - id: c1
    files:
      sub/setup.cfg: |
        [egg_info]
        tag_build = .dev0
`,
			want: true,
		},
		{
			name:    "MalformedSetupCfg",
			version: "1.0.0",
			repoYAML: `
commits:
  - id: c1
    files:
      setup.cfg: |
        [egg_info
        tag_build = .dev0
`,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := headTree(t, tc.repoYAML)
			got, err := NeedsSetupCfgSanitize(context.Background(), tree, tc.dir, tc.version)
			if err != nil {
				t.Fatalf("NeedsSetupCfgSanitize() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("NeedsSetupCfgSanitize() = %v, want %v", got, tc.want)
			}
		})
	}
}
