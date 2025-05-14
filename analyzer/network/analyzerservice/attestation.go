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

// NetworkRebuildParams defines the external parameters for network rebuild analysis.
type NetworkRebuildParams struct {
	// Ecosystem specifies the package ecosystem (e.g., npm, pypi, maven)
	Ecosystem string `json:"ecosystem"`
	// Package is the name of the package analyzed
	Package string `json:"package"`
	// Version is the specific version of the package analyzed
	Version string `json:"version"`
	// Artifact is the URI or identifier of the artifact analyzed
	Artifact string `json:"artifact"`
}

// NetworkRebuildDeps represents the resolved dependencies for network rebuild analysis.
type NetworkRebuildDeps struct {
	// AttestationBundle is the attestation from which this analysis was derived
	AttestationBundle slsa1.ResourceDescriptor
	// Source points to the source code repository descriptor
	Source *slsa1.ResourceDescriptor
	// Images contains container image descriptors used in the build
	Images []slsa1.ResourceDescriptor
}

// MarshalJSON flattens the deps into a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d NetworkRebuildDeps) MarshalJSON() ([]byte, error) {
	var rd []slsa1.ResourceDescriptor
	rd = append(rd, d.AttestationBundle)
	if d.Source != nil {
		rd = append(rd, *d.Source)
	}
	rd = append(rd, d.Images...)
	return json.Marshal(rd)
}

// UnmarshalJSON extracts deps from a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d *NetworkRebuildDeps) UnmarshalJSON(data []byte) error {
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

// NetworkRebuildByproducts contains the byproducts generated during network rebuild analysis.
type NetworkRebuildByproducts struct {
	// NetworkLog contains the network activity log from the rebuild
	NetworkLog slsa1.ResourceDescriptor
	// BuildStrategy contains the serialized strategy used for the rebuild
	BuildStrategy slsa1.ResourceDescriptor
	// BuildSteps contains the serialized Cloud Build steps executed
	BuildSteps slsa1.ResourceDescriptor
	// NOTE: We omit Dockerfile because we instrument it as part of the proxied
	// rebuild so the actual version will differ somewhat from the original.
}

// MarshalJSON flattens the byproducts into a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d NetworkRebuildByproducts) MarshalJSON() ([]byte, error) {
	return json.Marshal([]slsa1.ResourceDescriptor{d.NetworkLog, d.BuildStrategy})
}

// UnmarshalJSON extracts byproducts from a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d *NetworkRebuildByproducts) UnmarshalJSON(data []byte) error {
	var descriptors []slsa1.ResourceDescriptor
	if err := json.Unmarshal(data, &descriptors); err != nil {
		return err
	}
	if len(descriptors) != 2 {
		return errors.New("unexpected descriptor count")
	}
	for _, desc := range descriptors {
		switch {
		case strings.HasSuffix(desc.Name, string(NetworkLogAsset)):
			d.NetworkLog = desc
		case desc.Name == attestation.ByproductBuildStrategy:
			d.BuildStrategy = desc
		}
	}
	return nil
}

// NetworkRebuildBuildDef defines the build definition for network rebuild analysis.
type NetworkRebuildBuildDef struct {
	// BuildType is the NetworkRebuild build type identifier
	BuildType string `json:"buildType"`
	// ExternalParameters contains user-provided analysis parameters
	ExternalParameters NetworkRebuildParams `json:"externalParameters"`
	// InternalParameters contains service-internal configuration
	InternalParameters attestation.ServiceInternalParams `json:"internalParameters"`
	// ResolvedDependencies contains the dependencies resolved for this analysis
	ResolvedDependencies NetworkRebuildDeps `json:"resolvedDependencies"`
}

// NetworkRebuildRunDetails contains the runtime details of a network rebuild analysis.
type NetworkRebuildRunDetails struct {
	// Builder contains information about the build environment
	Builder slsa1.Builder `json:"builder"`
	// BuildMetadata contains metadata about the build execution
	BuildMetadata slsa1.BuildMetadata `json:"metadata"`
	// Byproducts contains artifacts generated during the analysis process
	Byproducts NetworkRebuildByproducts `json:"byproducts"`
}

// NetworkRebuildPredicate represents the predicate portion of a network rebuild analysis.
type NetworkRebuildPredicate struct {
	// BuildDefinition defines what was analyzed and how
	BuildDefinition NetworkRebuildBuildDef `json:"buildDefinition"`
	// RunDetails contains the actual execution information
	RunDetails NetworkRebuildRunDetails `json:"runDetails"`
}

// NetworkRebuildAttestation represents a complete network rebuild analysis statement.
type NetworkRebuildAttestation struct {
	// StatementHeader contains the standard in-toto statement header
	in_toto.StatementHeader `json:",inline"`
	// Predicate contains the network analysis-specific information
	Predicate NetworkRebuildPredicate `json:"predicate"`
}

// ToStatement converts the NetworkRebuildAttestation to a SLSA provenance statement.
func (nra *NetworkRebuildAttestation) ToStatement() (*in_toto.ProvenanceStatementSLSA1, error) {
	data, err := json.Marshal(nra)
	if err != nil {
		return nil, err
	}
	var s in_toto.ProvenanceStatementSLSA1
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
