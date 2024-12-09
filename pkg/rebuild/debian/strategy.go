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
	"fmt"
	"regexp"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

var (
	binaryVersionRegex = regexp.MustCompile(`^(?P<name>[^_]+)_(?P<nonbinary_version>[^_]+)(\+b\d+)_(?P<arch>[^_]+)\.deb$`)
)

type FileWithChecksum struct {
	URL string
	MD5 string
}

// DebianPackage aggregates the options controlling a debian package build.
type DebianPackage struct {
	DSC          FileWithChecksum
	Orig         FileWithChecksum
	Debian       FileWithChecksum
	Native       FileWithChecksum
	Requirements []string
}

var _ rebuild.Strategy = &DebianPackage{}

// Generate generates the instructions for a DebianPackage
func (b *DebianPackage) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	// TODO: use the FileWithChecksum.MD5 values to verify the downloaded archives.
	src, err := rebuild.PopulateTemplate(`
set -eux
wget {{.DSC.URL}}
{{- if .Native.URL }}
wget {{.Native.URL}}
{{ else }}
wget {{.Orig.URL}}
wget {{.Debian.URL}}
{{ end }}
dpkg-source -x --no-check $(basename "{{.DSC.URL}}")
	`, b)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	deps, err := rebuild.PopulateTemplate(`
set -eux
apt update
apt install -y {{join " " .Requirements}}
`, struct {
		DebianPackage
		BuildEnv rebuild.BuildEnv
		Target   rebuild.Target
	}{*b, be, t})
	if err != nil {
		return rebuild.Instructions{}, err
	}
	// If the target is a binary-only release (version ends with something like +b1) we need to add an additonal rename.
	var expected string
	if matches := binaryVersionRegex.FindStringSubmatch(t.Artifact); matches != nil {
		artifactName := matches[binaryVersionRegex.SubexpIndex("name")]
		nbversion := matches[binaryVersionRegex.SubexpIndex("nonbinary_version")]
		arch := matches[binaryVersionRegex.SubexpIndex("arch")]
		expected = fmt.Sprintf("%s_%s_%s.deb", artifactName, nbversion, arch)
	}
	build, err := rebuild.PopulateTemplate(`
set -eux
cd */
debuild -b -uc -us
{{- if .Expected }}
mv /src/{{ .Expected }} /src/{{ .Target.Artifact }}
{{- end }}
`, struct {
		Target   rebuild.Target
		Expected string
	}{Target: t, Expected: expected})
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return rebuild.Instructions{
		Location:   rebuild.Location{},
		Source:     src,
		Deps:       deps,
		Build:      build,
		SystemDeps: []string{"wget", "git", "build-essential", "fakeroot", "devscripts"},
		OutputPath: t.Artifact,
	}, nil
}
