// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"fmt"
	"time"

	"github.com/pkg/errors"
)

// Location defines the source code to be used for a rebuild, specifying the Git
// repository, a reference (like a commit hash or tag), and an optional
// subdirectory for executing the build and/or for expected output.
type Location struct {
	Repo string `json:"repo" yaml:"repo"`
	Ref  string `json:"ref" yaml:"ref"`
	Dir  string `json:"dir" yaml:"dir,omitempty"`
}

// RequiredEnv describes any required properties about the build environment
type RequiredEnv struct {
	SystemDeps []string `json:"system_deps" yaml:"system_deps,omitempty"`
}

// Instructions represents the source, dependencies, and build steps to execute a rebuild.
type Instructions struct {
	// The location these instructions should be executed from.
	Location Location
	Source   string
	Deps     string
	Build    string
	// Where the generated artifact can be found.
	OutputPath string
	Requires   RequiredEnv
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

// TimewarpURLFromString constructs the correct URL for an ecosystem and a point in time.
// For most ecosystems, this moment is defined as an RFC 3339-formatted timestamp.
// For cargo, this is defined as a git commit hash representing a historical registry state.
func (b *BuildEnv) TimewarpURLFromString(ecosystem string, registryMoment string) (string, error) {
	if ecosystem == "cargosparse" {
		if _, err := b.TimewarpURL(ecosystem, time.Now()); err != nil {
			return "", err
		}
		return fmt.Sprintf("sparse+http://%s:%s@%s/", ecosystem, registryMoment, b.TimewarpHost), nil
	} else if ecosystem == "cargogitarchive" {
		if _, err := b.TimewarpURL(ecosystem, time.Now()); err != nil {
			return "", err
		}
		return fmt.Sprintf("http://%s:%s@%s/", ecosystem, registryMoment, b.TimewarpHost), nil
	}
	registryTime, err := time.Parse(time.RFC3339, registryMoment)
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
