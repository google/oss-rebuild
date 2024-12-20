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
	"path/filepath"
	"regexp"

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

// Generate generates the instructions for a DebianPackage
func (b *DebianPackage) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	// TODO: use the FileWithChecksum.MD5 values to verify the downloaded archives.
	src, err := rebuild.PopulateTemplate(`
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

// Debrebuild uses the upstream's generated buildinfo to perform a rebuild.
type Debrebuild struct {
	BuildInfo FileWithChecksum
}

// Generate generates the instructions for a Debrebuild
func (b *Debrebuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	// TODO: use the FileWithChecksum.MD5 values to verify the downloaded buildinfo.
	inst := rebuild.Instructions{
		Location:   rebuild.Location{},
		SystemDeps: []string{"wget", "devscripts", "apt-utils", "mmdebstrap"},
		OutputPath: "out/" + t.Artifact,
	}
	var err error
	inst.Source, err = rebuild.PopulateTemplate(`
wget {{.BuildInfo.URL}}
`, b)
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
	inst.Build, err = rebuild.PopulateTemplate(`
debrebuild --buildresult=./out --builder=mmdebstrap {{ .BuildInfo }}
{{- if .Expected }}
mv /src/out/{{ .Expected }} /src/out/{{ .Target.Artifact }}
{{- end }}
`, struct {
		Target    rebuild.Target
		Expected  string
		BuildInfo string
	}{Target: t, Expected: expected, BuildInfo: filepath.Base(b.BuildInfo.URL)})
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return inst, nil
}
