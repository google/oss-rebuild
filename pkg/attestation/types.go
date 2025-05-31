// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package attestation

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/in-toto/in-toto-golang/in_toto"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
)

const (
	// BuildTypeRebuildV01 is the SLSA build type used for rebuild attestations.
	BuildTypeRebuildV01 = "https://docs.oss-rebuild.dev/builds/Rebuild@v0.1"
	// BuildTypeArtifactEquivalenceV01 is the SLSA build type used for artifact equivalence attestations.
	BuildTypeArtifactEquivalenceV01 = "https://docs.oss-rebuild.dev/builds/ArtifactEquivalence@v0.1"

	HostGoogle = "https://docs.oss-rebuild.dev/hosts/Google"

	DependencyBuildFix = "build.fix.json"

	ByproductBuildStrategy = "build.json"
	ByproductBuildSteps    = "steps.json"
	ByproductDockerfile    = "Dockerfile"
)

// SourceLocation describes a source code reference and optional path
type SourceLocation struct {
	// Path is the source repository relative path
	Path string `json:"path,omitempty"`
	// Ref is a descriptor of a source location (e.g. branch, tag, commit hash)
	Ref string `json:"ref"`
	// Repository is the source repository URI
	Repository string `json:"repository"`
}

// SourceLocationFromLocation creates a new SourceDescriptor instance from a rebuild.Location.
func SourceLocationFromLocation(loc rebuild.Location) SourceLocation {
	return SourceLocation{
		Repository: loc.Repo,
		Ref:        loc.Ref,
		Path:       loc.Dir,
	}
}

// ServiceInternalParams contains internal parameters for the rebuild service.
type ServiceInternalParams struct {
	// PrebuildSource is the prebuild resources source metadata
	PrebuildSource SourceLocation `json:"prebuildSource"`
	// ServiceSource is the service code source metadata
	ServiceSource SourceLocation `json:"serviceSource"`
}

// Rebuild attestation type definitions

// RebuildParams defines the external parameters required for a rebuild operation.
type RebuildParams struct {
	// Artifact is the URI or identifier of the artifact to rebuild
	Artifact string `json:"artifact"`
	// BuildConfigSource contains optional build configuration source information
	BuildConfigSource *SourceLocation `json:"buildConfigSource,omitempty"`
	// Ecosystem specifies the package ecosystem (e.g., npm, pypi, maven)
	Ecosystem string `json:"ecosystem"`
	// Package is the name of the package to rebuild
	Package string `json:"package"`
	// Version is the specific version of the package to rebuild
	Version string `json:"version"`
}

// RebuildDeps represents the resolved dependencies for a rebuild operation.
type RebuildDeps struct {
	// Source points to the source code repository descriptor
	// TODO: This should be mandatory
	Source *slsa1.ResourceDescriptor
	// Images contains container image descriptors used in the build
	Images []slsa1.ResourceDescriptor
	// BuildFix contains optional build fix descriptor for known issues
	BuildFix *slsa1.ResourceDescriptor
}

// MarshalJSON flattens the deps into a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d RebuildDeps) MarshalJSON() ([]byte, error) {
	var rd []slsa1.ResourceDescriptor
	if d.Source != nil {
		rd = append(rd, *d.Source)
	}
	rd = append(rd, d.Images...)
	if d.BuildFix != nil {
		rd = append(rd, *d.BuildFix)
	}
	return json.Marshal(rd)
}

// UnmarshalJSON extracts deps from a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d *RebuildDeps) UnmarshalJSON(data []byte) error {
	var descriptors []slsa1.ResourceDescriptor
	if err := json.Unmarshal(data, &descriptors); err != nil {
		return err
	}
	for _, desc := range descriptors {
		if strings.HasPrefix(desc.Name, "git+") {
			d.Source = &desc
		} else if desc.Name == DependencyBuildFix {
			d.BuildFix = &desc
		} else {
			d.Images = append(d.Images, desc)
		}
	}
	return nil
}

// RebuildByproducts contains the byproducts generated during a rebuild operation.
type RebuildByproducts struct {
	// BuildStrategy contains the serialized strategy used for the rebuild
	BuildStrategy slsa1.ResourceDescriptor
	// Dockerfile contains the Dockerfile used for the rebuild
	Dockerfile slsa1.ResourceDescriptor
	// BuildSteps contains the serialized Cloud Build steps executed
	BuildSteps slsa1.ResourceDescriptor
}

// MarshalJSON flattens the byproducts into a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d RebuildByproducts) MarshalJSON() ([]byte, error) {
	return json.Marshal([]slsa1.ResourceDescriptor{d.BuildStrategy, d.Dockerfile, d.BuildSteps})
}

// UnmarshalJSON extracts byproducts from a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d *RebuildByproducts) UnmarshalJSON(data []byte) error {
	var descriptors []slsa1.ResourceDescriptor
	if err := json.Unmarshal(data, &descriptors); err != nil {
		return err
	}
	if len(descriptors) != 3 {
		return errors.New("unexpected descriptor count")
	}
	for _, desc := range descriptors {
		switch desc.Name {
		case ByproductBuildStrategy:
			d.BuildStrategy = desc
		case ByproductBuildSteps:
			d.BuildSteps = desc
		case ByproductDockerfile:
			d.Dockerfile = desc
		}
	}
	return nil
}

// RebuildBuildDef defines the complete build definition for a rebuild operation.
type RebuildBuildDef struct {
	// BuildType is the Rebuild build type identifier
	BuildType string `json:"buildType"`
	// ExternalParameters contains user-provided rebuild parameters
	ExternalParameters RebuildParams `json:"externalParameters"`
	// InternalParameters contains service-internal configuration
	InternalParameters ServiceInternalParams `json:"internalParameters"`
	// ResolvedDependencies contains the dependencies resolved for this build
	ResolvedDependencies RebuildDeps `json:"resolvedDependencies"`
}

// RebuildRunDetails contains the runtime details of a rebuild operation.
type RebuildRunDetails struct {
	// Builder contains information about the build environment
	Builder slsa1.Builder `json:"builder"`
	// BuildMetadata contains metadata about the build execution
	BuildMetadata slsa1.BuildMetadata `json:"metadata"`
	// Byproducts contains artifacts generated during the build process
	Byproducts RebuildByproducts `json:"byproducts"`
}

// RebuildPredicate represents the predicate portion of a rebuild provenance.
type RebuildPredicate struct {
	// BuildDefinition defines what was built and how
	BuildDefinition RebuildBuildDef `json:"buildDefinition"`
	// RunDetails contains the actual execution information
	RunDetails RebuildRunDetails `json:"runDetails"`
}

// RebuildAttestation represents a complete rebuild provenance statement.
type RebuildAttestation struct {
	// StatementHeader contains the standard in-toto statement header
	in_toto.StatementHeader `json:",inline"`
	// Predicate contains the rebuild-specific provenance information
	Predicate RebuildPredicate `json:"predicate"`
}

// ToStatement converts the RebuildProvenance to a SLSA provenance statement.
func (rp *RebuildAttestation) ToStatement() (*in_toto.ProvenanceStatementSLSA1, error) {
	foo, err := json.Marshal(rp)
	if err != nil {
		return nil, err
	}
	var s in_toto.ProvenanceStatementSLSA1
	if err := json.Unmarshal(foo, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ArtifactEquivalence attestation type definitions

// ArtifactEquivalenceParams defines the parameters for artifact equivalence verification.
type ArtifactEquivalenceParams struct {
	// Candidate is the rebuilt artifact URI to be verified
	Candidate string `json:"candidate"`
	// Target is the upstream artifact URI to compare against
	Target string `json:"target"`
}

// ArtifactEquivalenceDeps represents the dependencies for artifact equivalence verification.
type ArtifactEquivalenceDeps struct {
	// RebuiltArtifact describes the artifact that was rebuilt
	RebuiltArtifact slsa1.ResourceDescriptor
	// UpstreamArtifact describes the original upstream artifact
	UpstreamArtifact slsa1.ResourceDescriptor
}

// MarshalJSON flattens the deps into a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d ArtifactEquivalenceDeps) MarshalJSON() ([]byte, error) {
	return json.Marshal([]slsa1.ResourceDescriptor{d.RebuiltArtifact, d.UpstreamArtifact})
}

// UnmarshalJSON extracts deps from a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d *ArtifactEquivalenceDeps) UnmarshalJSON(data []byte) error {
	var descriptors []slsa1.ResourceDescriptor
	if err := json.Unmarshal(data, &descriptors); err != nil {
		return err
	}
	if len(descriptors) != 2 {
		return errors.New("unexpected descriptor count")
	}
	for _, desc := range descriptors {
		if strings.HasPrefix(desc.Name, "rebuild/") {
			d.RebuiltArtifact = desc
		} else {
			d.UpstreamArtifact = desc
		}
	}
	return nil
}

// ArtifactEquivalenceByproducts contains byproducts from artifact equivalence verification.
type ArtifactEquivalenceByproducts struct {
	// NormalizedArtifact is the normalized candidate artifact used in comparison
	NormalizedArtifact slsa1.ResourceDescriptor
}

// MarshalJSON flattens the byproducts into a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d ArtifactEquivalenceByproducts) MarshalJSON() ([]byte, error) {
	return json.Marshal([]slsa1.ResourceDescriptor{d.NormalizedArtifact})
}

// UnmarshalJSON extracts byproducts from a ResourceDescriptors slice for compatibility with SLSA Provenance.
func (d *ArtifactEquivalenceByproducts) UnmarshalJSON(data []byte) error {
	var descriptors []slsa1.ResourceDescriptor
	if err := json.Unmarshal(data, &descriptors); err != nil {
		return err
	}
	if len(descriptors) != 1 {
		return errors.New("unexpected descriptor count")
	}
	d.NormalizedArtifact = descriptors[0]
	return nil
}

// ArtifactEquivalenceBuildDef defines the build definition for artifact equivalence verification.
type ArtifactEquivalenceBuildDef struct {
	// BuildType is the ArtifactEquivalence build type identifier
	BuildType string `json:"buildType"`
	// ExternalParameters contains the artifacts being compared
	ExternalParameters ArtifactEquivalenceParams `json:"externalParameters"`
	// InternalParameters contains service-internal configuration
	InternalParameters ServiceInternalParams `json:"internalParameters"`
	// ResolvedDependencies contains the artifacts involved in the comparison
	ResolvedDependencies ArtifactEquivalenceDeps `json:"resolvedDependencies"`
}

// ArtifactEquivalenceRunDetails contains the runtime details of an equivalence verification.
type ArtifactEquivalenceRunDetails struct {
	// Builder contains information about the verification environment
	Builder slsa1.Builder `json:"builder"`
	// BuildMetadata contains metadata about the verification execution
	BuildMetadata slsa1.BuildMetadata `json:"metadata"`
	// Byproducts contains artifacts generated during the verification process
	Byproducts ArtifactEquivalenceByproducts `json:"byproducts"`
}

// ArtifactEquivalencePredicate represents the predicate for artifact equivalence verification.
type ArtifactEquivalencePredicate struct {
	// BuildDefinition defines what equivalence check was performed and how
	BuildDefinition ArtifactEquivalenceBuildDef `json:"buildDefinition"`
	// RunDetails contains the actual verification execution information
	RunDetails ArtifactEquivalenceRunDetails `json:"runDetails"`
}

// ArtifactEquivalenceAttestation represents a complete artifact equivalence provenance statement.
type ArtifactEquivalenceAttestation struct {
	// StatementHeader contains the standard in-toto statement header
	in_toto.StatementHeader `json:",inline"`
	// Predicate contains the equivalence-specific provenance information
	Predicate ArtifactEquivalencePredicate `json:"predicate"`
}

// ToStatement converts the ArtifactEquivalenceProvenance to a SLSA provenance statement.
func (ap *ArtifactEquivalenceAttestation) ToStatement() (*in_toto.ProvenanceStatementSLSA1, error) {
	foo, err := json.Marshal(ap)
	if err != nil {
		return nil, err
	}
	var s in_toto.ProvenanceStatementSLSA1
	if err := json.Unmarshal(foo, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
