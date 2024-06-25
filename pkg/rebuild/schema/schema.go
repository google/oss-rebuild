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
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

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
	}
	if num != 1 {
		return nil, errors.Errorf("serialized StrategyOneOf should have exactly one strategy, found: %d", num)
	}
	return s, nil
}

type Message interface {
	ToValues() (url.Values, error)
}

// SmoketestRequest is a single request to the smoketest endpoint.
type SmoketestRequest struct {
	Ecosystem     rebuild.Ecosystem
	Package       string
	Versions      []string
	ID            string
	StrategyOneof *StrategyOneOf
}

var _ Message = &SmoketestRequest{}

// NewSmoketestRequest parses the smoketest form values into a SmoketestRequest.
func NewSmoketestRequest(form url.Values) (*SmoketestRequest, error) {
	req := &SmoketestRequest{}
	// TODO: check that it's a valid ecosystem?
	req.Ecosystem = rebuild.Ecosystem(form.Get("ecosystem"))
	if req.Ecosystem == "" {
		return nil, errors.New("No ecosystem provided")
	}
	req.Package = form.Get("pkg")
	if req.Package == "" {
		return nil, errors.New("No pkg provided")
	}
	versions := form.Get("versions")
	if versions != "" {
		req.Versions = strings.Split(versions, ",")
	}
	req.ID = form.Get("id")
	if req.ID == "" {
		return nil, errors.New("No ID provided")
	}
	if encStrat := form.Get("strategy"); encStrat != "" {
		oneof := StrategyOneOf{}
		err := json.Unmarshal([]byte(encStrat), &oneof)
		if err != nil {
			return nil, err
		}
		if _, err := oneof.Strategy(); err != nil {
			return nil, err
		}
		req.StrategyOneof = &oneof
	}
	return req, nil
}

// ToValues converts a SmoketestRequest into a url.Values.
func (sreq SmoketestRequest) ToValues() (url.Values, error) {
	vals := url.Values{}
	vals.Set("ecosystem", string(sreq.Ecosystem))
	vals.Set("pkg", sreq.Package)
	vals.Set("versions", strings.Join(sreq.Versions, ","))
	vals.Set("id", sreq.ID)
	if sreq.StrategyOneof != nil {
		encStrat, err := json.Marshal(sreq.StrategyOneof)
		if err != nil {
			return nil, err
		}
		vals.Set("strategy", string(encStrat))
	}
	return vals, nil
}

// ToInputs converts a SmoketestRequest into rebuild.Input objects.
func (sreq *SmoketestRequest) ToInputs() ([]rebuild.Input, error) {
	var inputs []rebuild.Input
	for _, v := range sreq.Versions {
		inputs = append(inputs, rebuild.Input{
			Target: rebuild.Target{
				Ecosystem: sreq.Ecosystem,
				Package:   sreq.Package,
				Version:   v,
			},
		})
	}
	if sreq.StrategyOneof != nil {
		if len(inputs) != 1 {
			return nil, errors.Errorf("strategy provided, expected exactly one version, got %d", len(sreq.Versions))
		}
		strategy, err := sreq.StrategyOneof.Strategy()
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
	Ecosystem        rebuild.Ecosystem
	Package          string
	Version          string
	ID               string
	StrategyFromRepo bool
}

var _ Message = &SmoketestRequest{}

// NewRebuildPackageRequest parses the rebuild form values into a RebuildPackageRequest.
func NewRebuildPackageRequest(form url.Values) (*RebuildPackageRequest, error) {
	req := &RebuildPackageRequest{}
	// TODO: check that it's a valid ecosystem?
	req.Ecosystem = rebuild.Ecosystem(form.Get("ecosystem"))
	if req.Ecosystem == "" {
		return nil, errors.New("No ecosystem provided")
	}
	req.Package = form.Get("pkg")
	if req.Package == "" {
		return nil, errors.New("No pkg provided")
	}
	version := form.Get("version")
	if version != "" {
		req.Version = version
	}
	req.ID = form.Get("id")
	if req.ID == "" {
		return nil, errors.New("No ID provided")
	}
	if fromRepo := form.Get("strategyFromRepo"); fromRepo != "" {
		strategyFromRepo, err := strconv.ParseBool(fromRepo)
		if err != nil {
			return nil, err
		}
		req.StrategyFromRepo = strategyFromRepo
	}
	return req, nil
}

// ToValues converts a RebuildPackageRequest into a url.Values.
func (req RebuildPackageRequest) ToValues() (url.Values, error) {
	vals := url.Values{}
	vals.Set("ecosystem", string(req.Ecosystem))
	vals.Set("pkg", req.Package)
	vals.Set("version", req.Version)
	vals.Set("id", req.ID)
	vals.Set("strategyFromRepo", strconv.FormatBool(req.StrategyFromRepo))
	return vals, nil
}

// InferenceRequest is a single request to the inference endpoint.
type InferenceRequest struct {
	Ecosystem    rebuild.Ecosystem
	Package      string
	Version      string
	LocationHint *rebuild.LocationHint
}

var _ Message = &InferenceRequest{}

// NewInferenceRequest parses the inference form values into an InferenceRequest.
func NewInferenceRequest(form url.Values) (*InferenceRequest, error) {
	req := &InferenceRequest{}
	// TODO: check that it's a valid ecosystem?
	req.Ecosystem = rebuild.Ecosystem(form.Get("ecosystem"))
	if req.Ecosystem == "" {
		return nil, errors.New("No ecosystem provided")
	}
	req.Package = form.Get("pkg")
	if req.Package == "" {
		return nil, errors.New("No pkg provided")
	}
	req.Version = form.Get("version")
	if req.Version == "" {
		return nil, errors.New("No version provided")
	}
	if encHint := form.Get("strategy_hint"); encHint != "" {
		var oneof StrategyOneOf
		err := json.Unmarshal([]byte(encHint), &oneof)
		if err != nil {
			return nil, err
		}
		var s rebuild.Strategy
		if s, err = oneof.Strategy(); err != nil {
			return nil, err
		} else if _, ok := s.(*rebuild.LocationHint); !ok {
			return nil, errors.Errorf("strategy hint should be a LocationHint, got: %T", s)
		}
		req.LocationHint = s.(*rebuild.LocationHint)
	}
	return req, nil
}

// ToValues converts an InferenceRequest into a url.Values.
func (req InferenceRequest) ToValues() (url.Values, error) {
	vals := url.Values{}
	vals.Set("ecosystem", string(req.Ecosystem))
	vals.Set("pkg", req.Package)
	vals.Set("version", req.Version)
	if req.LocationHint != nil {
		oneof := NewStrategyOneOf(req.LocationHint)
		hint, err := json.Marshal(oneof)
		if err != nil {
			return nil, err
		}
		vals.Set("strategy_hint", string(hint))
	}
	return vals, nil
}

// SmoketestAttempt stores rebuild and execution metadata on a single smoketest run.
type SmoketestAttempt struct {
	Ecosystem         string  `firestore:"ecosystem,omitempty"`
	Package           string  `firestore:"package,omitempty"`
	Version           string  `firestore:"version,omitempty"`
	Artifact          string  `firestore:"artifact,omitempty"`
	Success           bool    `firestore:"success,omitempty"`
	Message           string  `firestore:"message,omitempty"`
	Strategy          string  `firestore:"strategy,omitempty"`
	TimeCloneEstimate float64 `firestore:"time_clone_estimate,omitempty"`
	TimeSource        float64 `firestore:"time_source,omitempty"`
	TimeInfer         float64 `firestore:"time_infer,omitempty"`
	TimeBuild         float64 `firestore:"time_build,omitempty"`
	ExecutorVersion   string  `firestore:"executor_version,omitempty"`
	RunID             string  `firestore:"run_id,omitempty"`
	Created           int64   `firestore:"created,omitempty"`
}
