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

// Package schema is a set of utilities for marshalling strategies.
// Currently, schema only supports YAML but we may add protobuf in the future.
package schema

import (
	"encoding/hex"

	"github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// StrategyOneOf should contain exactly one strategy.
// The strategies are pointers because omitempty does not treat an empty struct as empty, but it
// does treat nil pointers as empty.
type StrategyOneOf struct {
	LocationHint         *rebuild.LocationHint          `json:"rebuild_location_hint,omitempty" yaml:"rebuild_location_hint,omitempty"`
	PureWheelBuild       *pypi.PureWheelBuild           `json:"pypi_pure_wheel_build,omitempty" yaml:"pypi_pure_wheel_build,omitempty"`
	NPMPackBuild         *npm.NPMPackBuild              `json:"npm_pack_build,omitempty" yaml:"npm_pack_build,omitempty"`
	NPMCustomBuild       *npm.NPMCustomBuild            `json:"npm_custom_build,omitempty" yaml:"npm_custom_build,omitempty"`
	CratesIOCargoPackage *cratesio.CratesIOCargoPackage `json:"cratesio_cargo_package,omitempty" yaml:"cratesio_cargo_package,omitempty"`
	ManualStrategy       *rebuild.ManualStrategy        `json:"manual,omitempty" yaml:"manual,omitempty"`
}

// NewStrategyOneOf creates a StrategyOneOf from a rebuild.Strategy, using typecasting to put the strategy in the right place.
func NewStrategyOneOf(s rebuild.Strategy) StrategyOneOf {
	oneof := StrategyOneOf{}
	switch t := s.(type) {
	case *rebuild.LocationHint:
		oneof.LocationHint = t
	case *pypi.PureWheelBuild:
		oneof.PureWheelBuild = t
	case *npm.NPMPackBuild:
		oneof.NPMPackBuild = t
	case *npm.NPMCustomBuild:
		oneof.NPMCustomBuild = t
	case *cratesio.CratesIOCargoPackage:
		oneof.CratesIOCargoPackage = t
	case *rebuild.ManualStrategy:
		oneof.ManualStrategy = t
	}
	return oneof
}

// Strategy returns the strategy contained inside the oneof, or an error if the wrong number are present.
func (oneof *StrategyOneOf) Strategy() (rebuild.Strategy, error) {
	var num int
	var s rebuild.Strategy
	{
		if oneof.LocationHint != nil {
			num++
			s = oneof.LocationHint
		}
		if oneof.PureWheelBuild != nil {
			num++
			s = oneof.PureWheelBuild
		}
		if oneof.NPMPackBuild != nil {
			num++
			s = oneof.NPMPackBuild
		}
		if oneof.NPMCustomBuild != nil {
			num++
			s = oneof.NPMCustomBuild
		}
		if oneof.CratesIOCargoPackage != nil {
			num++
			s = oneof.CratesIOCargoPackage
		}
		if oneof.ManualStrategy != nil {
			num++
			s = oneof.ManualStrategy
		}
	}
	if num != 1 {
		return nil, errors.Errorf("serialized StrategyOneOf should have exactly one strategy, found: %d", num)
	}
	return s, nil
}

type Message interface {
	Validate() error
}

type VersionRequest struct {
	Service string `form:","`
}

var _ Message = VersionRequest{}

func (VersionRequest) Validate() error { return nil }

type VersionResponse struct {
	Version string
}

// SmoketestRequest is a single request to the smoketest endpoint.
type SmoketestRequest struct {
	Ecosystem rebuild.Ecosystem `form:",required"`
	Package   string            `form:",required"`
	Versions  []string          `form:",required"`
	ID        string            `form:",required"`
	Strategy  *StrategyOneOf    `form:""`
}

var _ Message = SmoketestRequest{}

func (SmoketestRequest) Validate() error { return nil }

// ToInputs converts a SmoketestRequest into rebuild.Input objects.
func (req SmoketestRequest) ToInputs() ([]rebuild.Input, error) {
	var inputs []rebuild.Input
	for _, v := range req.Versions {
		inputs = append(inputs, rebuild.Input{
			Target: rebuild.Target{
				Ecosystem: req.Ecosystem,
				Package:   req.Package,
				Version:   v,
			},
		})
	}
	if req.Strategy != nil {
		if len(inputs) != 1 {
			return nil, errors.Errorf("strategy provided, expected exactly one version, got %d", len(req.Versions))
		}
		strategy, err := req.Strategy.Strategy()
		if err != nil {
			return nil, errors.Wrap(err, "parsing strategy in SmoketestRequest")
		}
		inputs[0].Strategy = strategy
	}
	return inputs, nil
}

type Verdict struct {
	Target        rebuild.Target
	Message       string
	StrategyOneof StrategyOneOf
	Timings       rebuild.Timings
}

// SmoketestResponse is the result of a rebuild smoketest.
type SmoketestResponse struct {
	Verdicts []Verdict
	Executor string
}

// RebuildPackageRequest is a single request to the rebuild package endpoint.
type RebuildPackageRequest struct {
	// TODO: Should this also include Artifact?
	Ecosystem        rebuild.Ecosystem `form:",required"`
	Package          string            `form:",required"`
	Version          string            `form:""`
	ID               string            `form:",required"`
	StrategyFromRepo bool              `form:""`
}

var _ Message = RebuildPackageRequest{}

func (RebuildPackageRequest) Validate() error { return nil }

// InferenceRequest is a single request to the inference endpoint.
type InferenceRequest struct {
	Ecosystem    rebuild.Ecosystem `form:",required"`
	Package      string            `form:",required"`
	Version      string            `form:",required"`
	StrategyHint *StrategyOneOf    `form:""`
}

var _ Message = InferenceRequest{}

func (req InferenceRequest) Validate() error {
	if req.StrategyHint == nil {
	} else if s, err := req.StrategyHint.Strategy(); err != nil {
		return err
	} else if _, ok := s.(*rebuild.LocationHint); !ok {
		return errors.Errorf("strategy hint should be a LocationHint, got: %T", s)
	}
	return nil
}

func (req InferenceRequest) LocationHint() *rebuild.LocationHint {
	s, _ := req.StrategyHint.Strategy()
	return s.(*rebuild.LocationHint)
}

type CreateRunRequest struct {
	Name string `form:","`
	Type string `form:","`
	Hash string `form:","`
}

var _ Message = CreateRunRequest{}

// Validate parses the CreateRun form values into a CreateRunRequest.
func (req CreateRunRequest) Validate() error {
	if _, err := hex.DecodeString(req.Hash); err != nil {
		return errors.Wrap(err, "decoding hex hash")
	}
	return nil
}

type CreateRunResponse struct {
	ID string
}

// SmoketestAttempt stores rebuild and execution metadata on a single smoketest run.
type SmoketestAttempt struct {
	Ecosystem       string          `firestore:"ecosystem,omitempty"`
	Package         string          `firestore:"package,omitempty"`
	Version         string          `firestore:"version,omitempty"`
	Artifact        string          `firestore:"artifact,omitempty"`
	Success         bool            `firestore:"success,omitempty"`
	Message         string          `firestore:"message,omitempty"`
	Strategy        StrategyOneOf   `firestore:"strategyoneof,omitempty"`
	Timings         rebuild.Timings `firestore:"timings,omitempty"`
	ExecutorVersion string          `firestore:"executor_version,omitempty"`
	RunID           string          `firestore:"run_id,omitempty"`
	Created         int64           `firestore:"created,omitempty"`
}
