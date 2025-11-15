// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package analyzerservice

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/oss-rebuild/pkg/attestation"
	"github.com/in-toto/in-toto-golang/in_toto"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
)

// SystemTraceRebuildParams defines the external parameters for system trace rebuild analysis.
type SystemTraceRebuildParams struct {
	// Ecosystem specifies the package ecosystem (e.g., npm, pypi, maven)
	Ecosystem string `json:"ecosystem"`
	// Package is the name of the package analyzed
	Package string `json:"package"`
	// Version is the specific version of the package analyzed
	Version string `json:"version"`
	// Artifact is the URI or identifier of the artifact analyzed
	Artifact string `json:"artifact"`
}

// SystemTraceRebuildDeps represents the resolved dependencies for system trace rebuild analysis.
type SystemTraceRebuildDeps struct {
	// AttestationBundle is the attestation from which this analysis was derived
	AttestationBundle slsa1.ResourceDescriptor
	// Source points to the source code repository descriptor
	Source *slsa1.ResourceDescriptor
	// Images contains container image descriptors used in the build
	Images []slsa1.ResourceDescriptor
}

// MarshalJSON flattens the deps into a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d SystemTraceRebuildDeps) MarshalJSON() ([]byte, error) {
	var rd []slsa1.ResourceDescriptor
	rd = append(rd, d.AttestationBundle)
	if d.Source != nil {
		rd = append(rd, *d.Source)
	}
	rd = append(rd, d.Images...)
	return json.Marshal(rd)
}

// UnmarshalJSON extracts deps from a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d *SystemTraceRebuildDeps) UnmarshalJSON(data []byte) error {
	var descriptors []slsa1.ResourceDescriptor
	if err := json.Unmarshal(data, &descriptors); err != nil {
		return err
	}
	for _, desc := range descriptors {
		if strings.HasPrefix(desc.Name, "git+") {
			d.Source = &desc
		} else {
			d.Images = append(d.Images, desc)
		}
	}
	return nil
}

// SystemTraceRebuildByproducts contains the byproducts generated during system trace rebuild analysis.
type SystemTraceRebuildByproducts struct {
	// SystemLog contains the system trace log from the rebuild
	SystemLog slsa1.ResourceDescriptor
	// BuildStrategy contains the serialized strategy used for the rebuild
	BuildStrategy slsa1.ResourceDescriptor
	// BuildSteps contains the serialized Cloud Build steps executed
	BuildSteps slsa1.ResourceDescriptor
	// NOTE: We omit Dockerfile because we instrument it as part of the monitored
	// rebuild so the actual version will differ somewhat from the original.
}

// MarshalJSON flattens the byproducts into a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d SystemTraceRebuildByproducts) MarshalJSON() ([]byte, error) {
	return json.Marshal([]slsa1.ResourceDescriptor{d.SystemLog, d.BuildStrategy})
}

// UnmarshalJSON extracts byproducts from a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d *SystemTraceRebuildByproducts) UnmarshalJSON(data []byte) error {
	var descriptors []slsa1.ResourceDescriptor
	if err := json.Unmarshal(data, &descriptors); err != nil {
		return err
	}
	if len(descriptors) != 2 {
		return errors.New("unexpected descriptor count")
	}
	for _, desc := range descriptors {
		switch {
		case strings.HasSuffix(desc.Name, string(SystemTraceAsset)):
			d.SystemLog = desc
		case desc.Name == attestation.ByproductBuildStrategy:
			d.BuildStrategy = desc
		}
	}
	return nil
}

// SystemTraceRebuildBuildDef defines the build definition for system trace rebuild analysis.
type SystemTraceRebuildBuildDef struct {
	// BuildType is the SystemTraceRebuild build type identifier
	BuildType string `json:"buildType"`
	// ExternalParameters contains user-provided analysis parameters
	ExternalParameters SystemTraceRebuildParams `json:"externalParameters"`
	// InternalParameters contains service-internal configuration
	InternalParameters attestation.ServiceInternalParams `json:"internalParameters"`
	// ResolvedDependencies contains the dependencies resolved for this analysis
	ResolvedDependencies SystemTraceRebuildDeps `json:"resolvedDependencies"`
}

// SystemTraceRebuildRunDetails contains the runtime details of a system trace rebuild analysis.
type SystemTraceRebuildRunDetails struct {
	// Builder contains information about the build environment
	Builder slsa1.Builder `json:"builder"`
	// BuildMetadata contains metadata about the build execution
	BuildMetadata slsa1.BuildMetadata `json:"metadata"`
	// Byproducts contains artifacts generated during the analysis process
	Byproducts SystemTraceRebuildByproducts `json:"byproducts"`
}

// SystemTraceRebuildPredicate represents the predicate portion of a system trace rebuild analysis.
type SystemTraceRebuildPredicate struct {
	// BuildDefinition defines what was analyzed and how
	BuildDefinition SystemTraceRebuildBuildDef `json:"buildDefinition"`
	// RunDetails contains the actual execution information
	RunDetails SystemTraceRebuildRunDetails `json:"runDetails"`
}

// SystemTraceRebuildAttestation represents a complete system trace rebuild analysis statement.
type SystemTraceRebuildAttestation struct {
	// StatementHeader contains the standard in-toto statement header
	in_toto.StatementHeader `json:",inline"`
	// Predicate contains the system trace analysis-specific information
	Predicate SystemTraceRebuildPredicate `json:"predicate"`
}

// ToStatement converts the SystemTraceRebuildAttestation to a SLSA provenance statement.
func (stra *SystemTraceRebuildAttestation) ToStatement() (*in_toto.ProvenanceStatementSLSA1, error) {
	data, err := json.Marshal(stra)
	if err != nil {
		return nil, err
	}
	var s in_toto.ProvenanceStatementSLSA1
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
