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

package maven

import "github.com/google/oss-rebuild/pkg/rebuild/rebuild"

type MavenBuild struct {
	rebuild.Location

	// JDKVersion is the version of the JDK to use for the build.
	JDKVersion string `json:"jdk_version" yaml:"jdk_version"`
}

var _ rebuild.Strategy = &MavenBuild{}

func (b *MavenBuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	return rebuild.Instructions{
		Location:   rebuild.Location{},
		Source:     "", // src
		Deps:       "", // deps
		Build:      "", // build
		SystemDeps: []string{"wget", "git", "build-essential", "fakeroot", "devscripts"},
		OutputPath: t.Artifact,
	}, nil
}
