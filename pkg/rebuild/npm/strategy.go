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

package npm

import (
	"path"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type NPMPackBuild struct {
	rebuild.Location
	// NPMVersion is the version of the NPM CLI to use for the build.
	NPMVersion string `json:"npm_version" yaml:"npm_version"`
	// VersionOverride provides an alternative version value to apply to the package.json file.
	VersionOverride string `json:"version_override" yaml:"version_override,omitempty"`
}

var _ rebuild.Strategy = &NPMPackBuild{}

// GenerateFor generates the instructions for a NPMPackBuild.
func (b *NPMPackBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	src, err := rebuild.BasicSourceSetup(b.Location, &be)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	deps := ""
	build, err := rebuild.PopulateTemplate(`
{{if ne .VersionOverride "" -}}
{{- /* NOTE: Use builtin npm for 'npm version' as it wasn't introduced until NPM v6. */ -}}
PATH=/usr/bin:/bin:/usr/local/bin /usr/bin/npm version --prefix {{.Location.Dir}} --no-git-tag-version {{.VersionOverride}}
{{end -}}
/usr/bin/npx --package=npm@{{.NPMVersion}} -c '{{if ne .Location.Dir "."}}cd {{.Location.Dir}} && {{end}}npm pack'
`, b)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return rebuild.Instructions{
		Location:   b.Location,
		SystemDeps: []string{"git", "npm"},
		Source:     src,
		Deps:       deps,
		Build:      build,
		OutputPath: path.Join(b.Location.Dir, t.Artifact),
	}, nil
}

// NPMCustomBuild implements a user-specified build script.
type NPMCustomBuild struct {
	rebuild.Location
	NPMVersion      string    `json:"npm_version" yaml:"npm_version"`
	NodeVersion     string    `json:"node_version" yaml:"node_version"`
	VersionOverride string    `json:"version_override,omitempty" yaml:"version_override,omitempty"`
	Command         string    `json:"command" yaml:"command"`
	RegistryTime    time.Time `json:"registry_time" yaml:"registry_time"`
}

var _ rebuild.Strategy = &NPMCustomBuild{}

// GenerateFor generates the instructions for a NPMCustomBuild.
func (b *NPMCustomBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	src, err := rebuild.BasicSourceSetup(b.Location, &be)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	buildAndEnv := struct {
		*NPMCustomBuild
		BuildEnv *rebuild.BuildEnv
	}{
		NPMCustomBuild: b,
		BuildEnv:       &be,
	}
	deps, err := rebuild.PopulateTemplate(`
/usr/bin/npm config --location-global set registry {{.BuildEnv.TimewarpURL "npm" .RegistryTime}}
trap '/usr/bin/npm config --location-global delete registry' EXIT
wget -O - https://unofficial-builds.nodejs.org/download/release/v{{.NodeVersion}}/node-v{{.NodeVersion}}-linux-x64-musl.tar.gz | tar xzf - --strip-components=1 -C /usr/local/
/usr/local/bin/npx --package=npm@{{.NPMVersion}} -c '{{if ne .Location.Dir "."}}cd {{.Location.Dir}} && {{end}}npm install --force'
`, buildAndEnv)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	build, err := rebuild.PopulateTemplate(`
{{if ne .VersionOverride "" -}}
{{- /* NOTE: Use builtin npm for 'npm version' as it wasn't introduced until NPM v6. */ -}}
PATH=/usr/bin:/bin:/usr/local/bin /usr/bin/npm version --prefix {{.Location.Dir}} --no-git-tag-version {{.VersionOverride}}
{{end -}}
/usr/local/bin/npx --package=npm@{{.NPMVersion}} -c '{{if ne .Location.Dir "."}}cd {{.Location.Dir}} && {{end}}npm run {{.Command}}' && rm -rf node_modules && npm pack
`, b)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return rebuild.Instructions{
		Location:   b.Location,
		SystemDeps: []string{"git", "npm"},
		Source:     src,
		Deps:       deps,
		Build:      build,
		OutputPath: path.Join(b.Location.Dir, t.Artifact),
	}, nil
}
