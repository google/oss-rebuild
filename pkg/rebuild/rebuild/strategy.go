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

package rebuild

import (
	"fmt"
	"time"

	"github.com/pkg/errors"
)

// Location is where a set of rebuild instruction should be executed.
type Location struct {
	Repo string `json:"repo" yaml:"repo"`
	Ref  string `json:"ref" yaml:"ref"`
	Dir  string `json:"dir" yaml:"dir,omitempty"`
}

// Instructions represents the source, dependencies, and build steps to execute a rebuild.
type Instructions struct {
	// The location these instructions should be executed from.
	Location   Location
	SystemDeps []string
	Source     string
	Deps       string
	Build      string
	// Where the generated artifact can be found.
	OutputPath string
}

// BuildEnv contains resources provided by the build environment that a strategy may use.
type BuildEnv struct {
	TimewarpHost           string
	HasRepo                bool
	PreferPreciseToolchain bool
}

// TimewarpURL constructs the correct URL for this ecosystem and registryTime.
func (b *BuildEnv) TimewarpURL(ecosystem string, registryTime time.Time) (string, error) {
	if b.TimewarpHost == "" {
		return "", errors.New("TimewarpHost hasn't been configured for this BuildEnv")
	}
	return fmt.Sprintf("http://%s:%s@%s", ecosystem, registryTime.Format(time.RFC3339), b.TimewarpHost), nil
}

// TimewarpURLFromString constructs the correct URL for an ecosystem and RFC 3339-formatted time.
func (b *BuildEnv) TimewarpURLFromString(ecosystem string, rfc3339Time string) (string, error) {
	registryTime, err := time.Parse(time.RFC3339, rfc3339Time)
	if err != nil {
		return "", errors.Wrap(err, "parsing time")
	}
	return b.TimewarpURL(ecosystem, registryTime)
}

// Strategy generates instructions to execute a rebuild.
type Strategy interface {
	GenerateFor(Target, BuildEnv) (Instructions, error)
}

// LocationHint is a partial strategy used to provide a hint (git repo, git ref) to the inference machinery, but it is not sufficient for execution.
type LocationHint struct {
	Location
}

// GenerateFor is unsupported for LocationHint.
func (s *LocationHint) GenerateFor(t Target, be BuildEnv) (Instructions, error) {
	return Instructions{}, errors.New("LocationHint must be expanded using inference")
}

var _ Strategy = &LocationHint{}
