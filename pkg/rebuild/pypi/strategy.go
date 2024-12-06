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

package pypi

import (
	"path"
	"time"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

// PureWheelBuild aggregates the options controlling a wheel build.
type PureWheelBuild struct {
	rebuild.Location
	Requirements []string  `json:"requirements"`
	RegistryTime time.Time `json:"registry_time" yaml:"registry_time,omitempty"`
}

var _ rebuild.Strategy = &PureWheelBuild{}

// GenerateFor generates the instructions for a PureWheelBuild.
func (b *PureWheelBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	src, err := rebuild.BasicSourceSetup(b.Location, &be)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	buildAndEnv := struct {
		*PureWheelBuild
		BuildEnv *rebuild.BuildEnv
	}{
		PureWheelBuild: b,
		BuildEnv:       &be,
	}
	deps, err := rebuild.PopulateTemplate(`
/usr/bin/python3 -m venv /deps
{{if not .RegistryTime.IsZero -}}
export PIP_INDEX_URL={{.BuildEnv.TimewarpURL "pypi" .RegistryTime}}
{{end -}}
/deps/bin/pip install build
{{range .Requirements -}}
/deps/bin/pip install {{.}}
{{end -}}
`, buildAndEnv)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	build, err := rebuild.PopulateTemplate("/deps/bin/python3 -m build --wheel -n {{.Location.Dir}}", b)
	if err != nil {
		return rebuild.Instructions{}, err
	}
	return rebuild.Instructions{
		Location:   b.Location,
		Source:     src,
		Deps:       deps,
		Build:      build,
		SystemDeps: []string{"git", "python3"},
		OutputPath: path.Join("dist", t.Artifact),
	}, nil
}
