// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package debian

import (
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
			Uses: "debian/build/package",
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

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

var toolkit = []*flow.Tool{
	{
		Name: "debian/fetch/sources",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				set -eux
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
		Name: "debian/deps/install",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				set -eux
				apt update
				apt install -y{{range $req := .With.requirements | fromJSON}} {{$req}}{{end}}`)[1:],
			Needs: []string{},
		}},
	},
	{
		Name: "debian/build/package",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				set -eux
				cd */
				debuild -b -uc -us
				{{- $expected := regexReplace .With.targetPath "\\+b[0-9]+(_[^_]+\\.deb)$" "$1"}}
				{{- if ne $expected .With.targetPath }}
				mv /src/{{$expected}} /src/{{.With.targetPath}}
				{{- end}}`)[1:],
			Needs: []string{"build-essential", "fakeroot", "devscripts"},
		}},
	},
}
