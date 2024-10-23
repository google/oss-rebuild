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
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
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
	Requirements []string
}

var _ rebuild.Strategy = &DebianPackage{}

// Generate generates the instructions for a DebianPackage
func (b *DebianPackage) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	// TODO: use the FileWithChecksum.MD5 values to verify the downloaded archives.
	src, err := rebuild.PopulateTemplate(`
set -eux
wget {{.DSC.URL}}
wget {{.Orig.URL}}
wget {{.Debian.URL}}
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
	build, err := rebuild.PopulateTemplate(`
set -eux
cd */
debuild -b -uc -us
`, t)
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
