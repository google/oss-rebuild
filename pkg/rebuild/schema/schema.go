// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package schema is a set of utilities for marshalling strategies.
// Currently, schema only supports YAML but we may add protobuf in the future.
package schema

import (
	"encoding/hex"
	"time"

	"github.com/google/oss-rebuild/internal/api"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/rebuild/cratesio"
	"github.com/google/oss-rebuild/pkg/rebuild/debian"
	"github.com/google/oss-rebuild/pkg/rebuild/maven"
	"github.com/google/oss-rebuild/pkg/rebuild/npm"
	"github.com/google/oss-rebuild/pkg/rebuild/pypi"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// StrategyOneOf should contain exactly one strategy.
// The strategies are pointers because omitempty does not treat an empty struct as empty, but it
// does treat nil pointers as empty.
type StrategyOneOf struct {
	LocationHint            *rebuild.LocationHint          `json:"rebuild_location_hint,omitempty" yaml:"rebuild_location_hint,omitempty"`
	PureWheelBuild          *pypi.PureWheelBuild           `json:"pypi_pure_wheel_build,omitempty" yaml:"pypi_pure_wheel_build,omitempty"`
	SourceDistributionBuild *pypi.SourceDistributionBuild  `json:"pypi_source_distribution_build,omitempty" yaml:"pypi_source_distribution_build,omitempty"`
	NPMPackBuild            *npm.NPMPackBuild              `json:"npm_pack_build,omitempty" yaml:"npm_pack_build,omitempty"`
	NPMCustomBuild          *npm.NPMCustomBuild            `json:"npm_custom_build,omitempty" yaml:"npm_custom_build,omitempty"`
	CratesIOCargoPackage    *cratesio.CratesIOCargoPackage `json:"cratesio_cargo_package,omitempty" yaml:"cratesio_cargo_package,omitempty"`
	MavenBuild              *maven.MavenBuild              `json:"maven_build,omitempty" yaml:"maven_build,omitempty"`
	DebianPackage           *debian.DebianPackage          `json:"debian_package,omitempty" yaml:"debian_package,omitempty"`
	Debrebuild              *debian.Debrebuild             `json:"debrebuild,omitempty" yaml:"debrebuild,omitempty"`
	ManualStrategy          *rebuild.ManualStrategy        `json:"manual,omitempty" yaml:"manual,omitempty"`
	WorkflowStrategy        *rebuild.WorkflowStrategy      `json:"flow,omitempty" yaml:"flow,omitempty"`
}

// NewStrategyOneOf creates a StrategyOneOf from a rebuild.Strategy, using typecasting to put the strategy in the right place.
func NewStrategyOneOf(s rebuild.Strategy) StrategyOneOf {
	oneof := StrategyOneOf{}
	switch t := s.(type) {
	case *rebuild.LocationHint:
		oneof.LocationHint = t
	case *pypi.PureWheelBuild:
		oneof.PureWheelBuild = t
	case *pypi.SourceDistributionBuild:
		oneof.SourceDistributionBuild = t
	case *maven.MavenBuild:
		oneof.MavenBuild = t
	case *npm.NPMPackBuild:
		oneof.NPMPackBuild = t
	case *npm.NPMCustomBuild:
		oneof.NPMCustomBuild = t
	case *cratesio.CratesIOCargoPackage:
		oneof.CratesIOCargoPackage = t
	case *debian.DebianPackage:
		oneof.DebianPackage = t
	case *debian.Debrebuild:
		oneof.Debrebuild = t
	case *rebuild.ManualStrategy:
		oneof.ManualStrategy = t
	case *rebuild.WorkflowStrategy:
		oneof.WorkflowStrategy = t
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
		if oneof.SourceDistributionBuild != nil {
			num++
			s = oneof.SourceDistributionBuild
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
		if oneof.DebianPackage != nil {
			num++
			s = oneof.DebianPackage
		}
		if oneof.Debrebuild != nil {
			num++
			s = oneof.Debrebuild
		}
		if oneof.ManualStrategy != nil {
			num++
			s = oneof.ManualStrategy
		}
		if oneof.WorkflowStrategy != nil {
			num++
			s = oneof.WorkflowStrategy
		}
	}
	if num != 1 {
		return nil, errors.Errorf("serialized StrategyOneOf should have exactly one strategy, found: %d", num)
	}
	return s, nil
}

type BuildDefinition struct {
	*StrategyOneOf    `json:",inline,omitempty" yaml:",inline,omitempty"`
	CustomStabilizers []archive.CustomStabilizerEntry `json:"custom_stabilizers,omitempty" yaml:"custom_stabilizers,omitempty"`
}

type VersionRequest struct {
	Service string `form:","`
}

var _ api.Message = VersionRequest{}

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

var _ api.Message = SmoketestRequest{}

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
	Ecosystem         rebuild.Ecosystem `form:",required"`
	Package           string            `form:",required"`
	Version           string            `form:",required"`
	Artifact          string            `form:""`
	ID                string            `form:",required"`
	UseRepoDefinition bool              `form:""`
	UseSyscallMonitor bool              `form:""`
	UseNetworkProxy   bool              `form:""`
	BuildTimeout      time.Duration     `form:""`
}

var _ api.Message = RebuildPackageRequest{}

func (RebuildPackageRequest) Validate() error { return nil }

// InferenceRequest is a single request to the inference endpoint.
type InferenceRequest struct {
	Ecosystem    rebuild.Ecosystem `form:",required"`
	Package      string            `form:",required"`
	Version      string            `form:",required"`
	Artifact     string            `form:""`
	StrategyHint *StrategyOneOf    `form:""`
}

var _ api.Message = InferenceRequest{}

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
	if req.StrategyHint == nil {
		return nil
	}
	s, _ := req.StrategyHint.Strategy()
	return s.(*rebuild.LocationHint)
}

type CreateRunRequest struct {
	BenchmarkName string `form:","`
	BenchmarkHash string `form:","`
	Type          string `form:","`
}

var _ api.Message = CreateRunRequest{}

// Validate parses the CreateRun form values into a CreateRunRequest.
func (req CreateRunRequest) Validate() error {
	if _, err := hex.DecodeString(req.BenchmarkHash); err != nil {
		return errors.Wrap(err, "decoding hex hash")
	}
	return nil
}

// RebuildAttempt stores rebuild and execution metadata on a single smoketest run.
type RebuildAttempt struct {
	Ecosystem       string          `firestore:"ecosystem,omitempty"`
	Package         string          `firestore:"package,omitempty"`
	Version         string          `firestore:"version,omitempty"`
	Artifact        string          `firestore:"artifact,omitempty"`
	Success         bool            `firestore:"success,omitempty"`
	Message         string          `firestore:"message,omitempty"`
	Strategy        StrategyOneOf   `firestore:"strategyoneof,omitempty"`
	Dockerfile      string          `firestore:"dockerfile,omitempty"`
	Timings         rebuild.Timings `firestore:"timings,omitempty"`
	ExecutorVersion string          `firestore:"executor_version,omitempty"`
	RunID           string          `firestore:"run_id,omitempty"`
	BuildID         string          `firestore:"build_id,omitempty"`
	ObliviousID     string          `firestore:"oblivious_id,omitempty"`
	Started         time.Time       `firestore:"started,omitempty"` // The time rebuild started
	Created         time.Time       `firestore:"created,omitempty"` // The time this record was created
}

// Run stores metadata on an execution grouping.
type Run struct {
	ID            string    `firestore:"id,omitempty"`
	BenchmarkName string    `firestore:"benchmark_name,omitempty"`
	BenchmarkHash string    `firestore:"benchmark_hash,omitempty"`
	Type          string    `firestore:"run_type,omitempty"`
	Created       time.Time `firestore:"created,omitempty"`
}

type ReleaseEvent struct {
	Ecosystem rebuild.Ecosystem `form:",required"`
	Package   string            `form:",required"`
	Version   string            `form:",required"`
	Artifact  string            `form:""`
}

func (ReleaseEvent) Validate() error { return nil }

func (e ReleaseEvent) From(t rebuild.Target) ReleaseEvent {
	e.Ecosystem = t.Ecosystem
	e.Package = t.Package
	e.Version = t.Version
	e.Artifact = t.Artifact
	return e
}

var _ api.Message = ReleaseEvent{}

// AnalyzeRebuildRequest is a request to analyze a rebuilt package.
type AnalyzeRebuildRequest struct {
	Ecosystem rebuild.Ecosystem `form:",required"`
	Package   string            `form:",required"`
	Version   string            `form:",required"`
	Artifact  string            `form:",required"`
	Extras    string            `form:""`
	Timeout   time.Duration     `form:""`
}

var _ api.Message = AnalyzeRebuildRequest{}

func (req AnalyzeRebuildRequest) Validate() error { return nil }

// Execution mode describes the manner in which a rebuild happens.
type ExecutionMode string

const (
	SmoketestMode ExecutionMode = "smoketest" // No attestations, faster.
	AttestMode    ExecutionMode = "attest"    // Creates attestations, slower.
)
