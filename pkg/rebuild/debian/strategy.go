// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package debian

import (
	"encoding/base64"
	"fmt"
	"path"
	"regexp"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

var (
	binaryVersionRegex = regexp.MustCompile(`^(?P<name>[^_]+)_(?P<nonbinary_version>[^_]+)(\+b\d+)_(?P<arch>[^_]+)\.deb$`)
)

type FileWithChecksum struct {
	URL string `json:"url" yaml:"url,omitempty"`
	MD5 string `json:"md5" yaml:"md5,omitempty"`
}

// DebianPackage aggregates the options controlling a debian package build.
type DebianPackage struct {
	DSC          FileWithChecksum `json:"dsc" yaml:"dsc,omitempty"`
	Orig         FileWithChecksum `json:"orig" yaml:"orig,omitempty"`
	Debian       FileWithChecksum `json:"debian" yaml:"debian,omitempty"`
	Native       FileWithChecksum `json:"native" yaml:"native,omitempty"`
	Requirements []string         `json:"requirements" yaml:"requirements,omitempty"`
}

var _ rebuild.Strategy = &DebianPackage{}

func (b *DebianPackage) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Source: []flow.Step{{
			Uses: "debian/fetch/sources",
			With: map[string]string{
				"dscUrl":    b.DSC.URL,
				"origUrl":   b.Orig.URL,
				"debianUrl": b.Debian.URL,
				"nativeUrl": b.Native.URL,
				"dscMd5":    b.DSC.MD5,
				"origMd5":   b.Orig.MD5,
				"debianMd5": b.Debian.MD5,
				"nativeMd5": b.Native.MD5,
			},
		}},
		Deps: []flow.Step{{
			Uses: "debian/deps/install",
			With: map[string]string{
				"requirements": flow.MustToJSON(b.Requirements),
			},
		}},
		Build: []flow.Step{{
			Uses: "debian/build/debuild",
			With: map[string]string{
				"targetPath": "{{.Target.Artifact}}",
			},
		}},
	}
}

// GenerateFor generates the instructions for a DebianPackage
func (b *DebianPackage) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

// Debrebuild uses the upstream's generated buildinfo to perform a rebuild.
type Debrebuild struct {
	BuildInfo FileWithChecksum `json:"buildinfo" yaml:"buildinfo,omitempty"`
}

func (b *Debrebuild) ToWorkflow() *rebuild.WorkflowStrategy {
	return &rebuild.WorkflowStrategy{
		Source: []flow.Step{{
			Uses: "debian/fetch/buildinfo",
			With: map[string]string{
				"buildinfoUrl": b.BuildInfo.URL,
				"buildinfoMd5": b.BuildInfo.MD5,
			},
		}},
		Deps: []flow.Step{{
			Uses: "debian/deps/patch-debrebuild",
		}},
		Build: []flow.Step{{
			Uses: "debian/build/debrebuild",
			With: map[string]string{
				"targetPath": "{{.Target.Artifact}}",
				"buildinfo":  path.Base(b.BuildInfo.URL),
			},
		}},
		OutputDir: "out",
	}
}

// Generate generates the instructions for a Debrebuild
func (b *Debrebuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

var toolkit = []*flow.Tool{
	{
		Name: "debian/fetch/sources",
		Steps: []flow.Step{{
			// TODO: use the FileWithChecksum.MD5 values to verify the downloaded archives.
			Runs: textwrap.Dedent(`
				wget {{.With.dscUrl}}
				{{- if ne .With.nativeUrl "" }}
				wget {{.With.nativeUrl}}
				{{ else }}
				wget {{.With.origUrl}}
				wget {{.With.debianUrl}}
				{{ end }}
				dpkg-source -x --no-check $(basename "{{.With.dscUrl}}")`)[1:],
			Needs: []string{"wget", "git"},
		}},
	},
	{
		Name: "debian/fetch/buildinfo",
		Steps: []flow.Step{{
			// TODO: use the FileWithChecksum.MD5 values to verify the downloaded archives.
			Runs: textwrap.Dedent(`
				wget {{.With.buildinfoUrl}}`)[1:],
			Needs: []string{"wget"},
		}},
	},
	{
		Name: "debian/deps/install",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				apt update
				apt install -y{{range $req := .With.requirements | fromJSON}} {{$req}}{{end}}`)[1:],
			Needs: []string{},
		}},
	},
	{
		Name: "debian/deps/patch-debrebuild",
		Steps: []flow.Step{{
			Runs: fmt.Sprintf(`echo %s | base64 -d | patch /usr/bin/debrebuild`, base64.StdEncoding.EncodeToString([]byte(`@@ -725,2 +725,3 @@
         ),
+        '--customize-hook=sleep 10',
         '--customize-hook=chroot "$1" sh -c "'`))),
			Needs: []string{"devscripts=2.25.2"},
		}},
	},
	{
		Name: "debian/build/handle-binary-version",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				{{- $expected := regexReplace .With.targetPath "\\+b[0-9]+(_[^_]+\\.deb)$" "$1"}}
				{{- if ne $expected .With.targetPath }}mv /src/{{$expected}} /src/{{.With.targetPath}}
				{{- end}}`)[1:],
			Needs: []string{},
		}},
	},
	{
		Name: "debian/build/debuild",
		Steps: []flow.Step{
			{
				Runs: textwrap.Dedent(`
					cd */
					debuild -b -uc -us`)[1:],
				Needs: []string{"build-essential", "fakeroot", "devscripts"},
			},
			{
				Uses: "debian/build/handle-binary-version",
				With: map[string]string{
					"targetPath": "{{.With.targetPath}}",
				},
			},
		},
	},
	{
		Name: "debian/build/debrebuild",
		Steps: []flow.Step{
			{
				Runs:  "debrebuild --buildresult=./out --builder=mmdebstrap {{ .With.buildinfo }}",
				Needs: []string{"devscripts=2.25.2", "apt-utils", "mmdebstrap"},
			},
		},
	},
}
