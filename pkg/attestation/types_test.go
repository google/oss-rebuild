// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package attestation

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
)

func TestSourceLocationFromLocation(t *testing.T) {
	tests := []struct {
		name     string
		input    rebuild.Location
		expected SourceLocation
	}{
		{
			name: "complete location",
			input: rebuild.Location{
				Repo: "https://github.com/example/repo",
				Ref:  "main",
				Dir:  "src/package",
			},
			expected: SourceLocation{
				Repository: "https://github.com/example/repo",
				Ref:        "main",
				Path:       "src/package",
			},
		},
		{
			name: "minimal location",
			input: rebuild.Location{
				Repo: "https://github.com/example/repo",
				Ref:  "v1.0.0",
			},
			expected: SourceLocation{
				Repository: "https://github.com/example/repo",
				Ref:        "v1.0.0",
				Path:       "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SourceLocationFromLocation(tt.input)
			if diff := cmp.Diff(tt.expected, result); diff != "" {
				t.Errorf("SourceLocationFromLocation() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRebuildDeps_JSONRoundtrip(t *testing.T) {
	tests := []struct {
		name  string
		input RebuildDeps
	}{
		{
			name: "complete dependencies",
			input: RebuildDeps{
				Source: &slsa1.ResourceDescriptor{
					Name:   "git+https://github.com/example/repo",
					URI:    "https://github.com/example/repo@main",
					Digest: common.DigestSet{"sha1": "abc123def456"},
				},
				Images: []slsa1.ResourceDescriptor{
					{
						Name:   "docker.io/library/node:18",
						URI:    "docker.io/library/node@sha256:abc123",
						Digest: common.DigestSet{"sha256": "abc123def456789"},
					},
					{
						Name:   "docker.io/library/alpine:3.18",
						URI:    "docker.io/library/alpine@sha256:def456",
						Digest: common.DigestSet{"sha256": "def456abc123789"},
					},
				},
				BuildFix: &slsa1.ResourceDescriptor{
					Name:   DependencyBuildFix,
					URI:    "https://example.com/buildfix.patch",
					Digest: common.DigestSet{"sha256": "buildfix123456"},
				},
			},
		},
		{
			name: "only images",
			input: RebuildDeps{
				Images: []slsa1.ResourceDescriptor{
					{
						Name: "docker.io/library/node:18",
						URI:  "docker.io/library/node@sha256:abc123",
					},
				},
			},
		},
		{
			name: "source and images only",
			input: RebuildDeps{
				Source: &slsa1.ResourceDescriptor{
					Name: "git+https://github.com/example/repo",
					URI:  "https://github.com/example/repo@main",
				},
				Images: []slsa1.ResourceDescriptor{
					{
						Name: "docker.io/library/node:18",
						URI:  "docker.io/library/node@sha256:abc123",
					},
				},
			},
		},
		{
			name:  "empty dependencies",
			input: RebuildDeps{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
			var restored RebuildDeps
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if diff := cmp.Diff(tt.input, restored); diff != "" {
				t.Errorf("JSON roundtrip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRebuildByproducts_JSONRoundtrip(t *testing.T) {
	tests := []struct {
		name  string
		input RebuildByproducts
	}{
		{
			name: "complete byproducts",
			input: RebuildByproducts{
				BuildStrategy: slsa1.ResourceDescriptor{
					Name:   ByproductBuildStrategy,
					URI:    "https://example.com/strategy.json",
					Digest: common.DigestSet{"sha256": "strategy123456"},
				},
				Dockerfile: slsa1.ResourceDescriptor{
					Name:   ByproductDockerfile,
					URI:    "https://example.com/Dockerfile",
					Digest: common.DigestSet{"sha256": "dockerfile123456"},
				},
				BuildSteps: slsa1.ResourceDescriptor{
					Name:   ByproductBuildSteps,
					URI:    "https://example.com/steps.yaml",
					Digest: common.DigestSet{"sha256": "steps123456"},
				},
			},
		},
		{
			name: "minimal byproducts",
			input: RebuildByproducts{
				BuildStrategy: slsa1.ResourceDescriptor{
					Name: ByproductBuildStrategy,
					URI:  "https://example.com/strategy.json",
				},
				Dockerfile: slsa1.ResourceDescriptor{
					Name: ByproductDockerfile,
					URI:  "https://example.com/Dockerfile",
				},
				BuildSteps: slsa1.ResourceDescriptor{
					Name: ByproductBuildSteps,
					URI:  "https://example.com/steps.yaml",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
			var restored RebuildByproducts
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if diff := cmp.Diff(tt.input, restored); diff != "" {
				t.Errorf("JSON roundtrip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRebuildByproducts_UnmarshalErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "wrong descriptor count - too few",
			input: `[{"name": "only-one"}]`,
		},
		{
			name:  "wrong descriptor count - too many",
			input: `[{"name": "one"}, {"name": "two"}, {"name": "three"}, {"name": "four"}]`,
		},
		{
			name:  "invalid json",
			input: `invalid json`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var restored RebuildByproducts
			err := json.Unmarshal([]byte(tt.input), &restored)
			if err == nil {
				t.Error("Expected error but got none")
			}
		})
	}
}

func TestArtifactEquivalenceDeps_JSONRoundtrip(t *testing.T) {
	tests := []struct {
		name  string
		input ArtifactEquivalenceDeps
	}{
		{
			name: "complete equivalence deps",
			input: ArtifactEquivalenceDeps{
				RebuiltArtifact: slsa1.ResourceDescriptor{
					Name:   "rebuild/package.tar.gz",
					Digest: common.DigestSet{"sha256": "rebuilt123456"},
				},
				UpstreamArtifact: slsa1.ResourceDescriptor{
					Name:   "https://registry.npmjs.org/package/-/package-1.0.0.tgz",
					Digest: common.DigestSet{"sha256": "upstream123456"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
			var restored ArtifactEquivalenceDeps
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if diff := cmp.Diff(tt.input, restored); diff != "" {
				t.Errorf("JSON roundtrip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestArtifactEquivalenceByproducts_JSONRoundtrip(t *testing.T) {
	tests := []struct {
		name  string
		input ArtifactEquivalenceByproducts
	}{
		{
			name: "complete byproducts",
			input: ArtifactEquivalenceByproducts{
				StabilizedArtifact: slsa1.ResourceDescriptor{
					Name:   "stabilized/package.tar.gz",
					Digest: common.DigestSet{"sha256": "stabilized123456"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}
			var restored ArtifactEquivalenceByproducts
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if diff := cmp.Diff(tt.input, restored); diff != "" {
				t.Errorf("JSON roundtrip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestArtifactEquivalenceByproducts_UnmarshalErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "wrong descriptor count - too few",
			input: `[]`,
		},
		{
			name:  "wrong descriptor count - too many",
			input: `[{"name": "one"}, {"name": "two"}, {"name": "three"}, {"name": "four"}]`,
		},
		{
			name:  "invalid json",
			input: `invalid json`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var restored ArtifactEquivalenceByproducts
			err := json.Unmarshal([]byte(tt.input), &restored)
			if err == nil {
				t.Error("Expected error but got none")
			}
		})
	}
}

func TestRebuildAttestation_SLSACompatibility(t *testing.T) {
	tests := []struct {
		name        string
		attestation RebuildAttestation
	}{
		{
			name: "complete rebuild attestation",
			attestation: RebuildAttestation{
				StatementHeader: in_toto.StatementHeader{
					Type:          "https://in-toto.io/Statement/v1",
					PredicateType: "https://slsa.dev/provenance/v1",
					Subject: []in_toto.Subject{
						{
							Name:   "example-package",
							Digest: common.DigestSet{"sha256": "abc123def456"},
						},
					},
				},
				Predicate: RebuildPredicate{
					BuildDefinition: RebuildBuildDef{
						BuildType: "https://docs.oss-rebuild.dev/builds/Rebuild@v0.1",
						ExternalParameters: RebuildParams{
							Artifact:  "example-1.0.0.tgz",
							Ecosystem: "npm",
							Package:   "example",
							Version:   "1.0.0",
							BuildConfigSource: &SourceLocation{
								Repository: "https://github.com/foo/oss-rebuild",
								Ref:        "v1.0.0",
							},
						},
						InternalParameters: ServiceInternalParams{
							PrebuildSource: SourceLocation{
								Repository: "https://github.com/google/oss-rebuild",
								Ref:        "main",
							},
							ServiceSource: SourceLocation{
								Repository: "https://github.com/google/oss-rebuild",
								Ref:        "abcdef",
							},
						},
						ResolvedDependencies: RebuildDeps{
							Source: &slsa1.ResourceDescriptor{
								Name:   "git+https://github.com/example/repo",
								Digest: common.DigestSet{"sha1": "foobar"},
							},
							Images: []slsa1.ResourceDescriptor{
								{
									Name:   "docker.io/library/node:18",
									Digest: common.DigestSet{"sha256": "abc123"},
								},
							},
						},
					},
					RunDetails: RebuildRunDetails{
						Builder: slsa1.Builder{
							ID: "https://github.com/google/oss-rebuild",
						},
						BuildMetadata: slsa1.BuildMetadata{
							InvocationID: "test-invocation-123",
						},
						Byproducts: RebuildByproducts{
							BuildStrategy: slsa1.ResourceDescriptor{
								Name: ByproductBuildStrategy,
								URI:  "https://example.com/strategy.json",
							},
							Dockerfile: slsa1.ResourceDescriptor{
								Name: ByproductDockerfile,
								URI:  "https://example.com/Dockerfile",
							},
							BuildSteps: slsa1.ResourceDescriptor{
								Name: ByproductBuildSteps,
								URI:  "https://example.com/steps.yaml",
							},
						},
					},
				},
			},
		},
		{
			name: "minimal rebuild attestation",
			attestation: RebuildAttestation{
				StatementHeader: in_toto.StatementHeader{
					Type:          "https://in-toto.io/Statement/v1",
					PredicateType: "https://slsa.dev/provenance/v1",
					Subject: []in_toto.Subject{
						{
							Name:   "minimal-package",
							Digest: common.DigestSet{"sha256": "minimal123456"},
						},
					},
				},
				Predicate: RebuildPredicate{
					BuildDefinition: RebuildBuildDef{
						BuildType: "https://docs.oss-rebuild.dev/builds/Rebuild@v0.1",
						ExternalParameters: RebuildParams{
							Artifact:  "https://registry.npmjs.org/minimal/-/minimal-1.0.0.tgz",
							Ecosystem: "npm",
							Package:   "minimal",
							Version:   "1.0.0",
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.attestation)
			if err != nil {
				t.Fatalf("Marshal attestation failed: %v", err)
			}
			var restored RebuildAttestation
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatalf("Unmarshal attestation failed: %v", err)
			}
			if diff := cmp.Diff(tt.attestation, restored); diff != "" {
				t.Errorf("Attestation JSON roundtrip mismatch (-want +got):\n%s", diff)
			}
			statement, err := tt.attestation.ToStatement()
			if err != nil {
				t.Fatalf("ToStatement failed: %v", err)
			}
			if statement == nil {
				t.Fatal("ToStatement returned nil")
			}
			if statement.Type != tt.attestation.Type {
				t.Errorf("Statement type mismatch: want %s, got %s", tt.attestation.Type, statement.Type)
			}
			if statement.PredicateType != tt.attestation.PredicateType {
				t.Errorf("Statement predicate type mismatch: want %s, got %s", tt.attestation.PredicateType, statement.PredicateType)
			}
			slsaData, err := json.Marshal(statement)
			if err != nil {
				t.Fatalf("Marshal SLSA statement failed: %v", err)
			}
			var restoredSLSA in_toto.ProvenanceStatementSLSA1
			if err := json.Unmarshal(slsaData, &restoredSLSA); err != nil {
				t.Fatalf("Unmarshal SLSA statement failed: %v", err)
			}
			if diff := cmp.Diff(*statement, restoredSLSA); diff != "" {
				t.Errorf("SLSA statement JSON roundtrip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestArtifactEquivalenceAttestation_SLSACompatibility(t *testing.T) {
	tests := []struct {
		name        string
		attestation ArtifactEquivalenceAttestation
	}{
		{
			name: "complete equivalence attestation",
			attestation: ArtifactEquivalenceAttestation{
				StatementHeader: in_toto.StatementHeader{
					Type:          "https://in-toto.io/Statement/v1",
					PredicateType: "https://slsa.dev/provenance/v1",
					Subject: []in_toto.Subject{
						{
							Name:   "equivalence-result",
							Digest: common.DigestSet{"sha256": "equivalence123456"},
						},
					},
				},
				Predicate: ArtifactEquivalencePredicate{
					BuildDefinition: ArtifactEquivalenceBuildDef{
						BuildType: "https://docs.oss-rebuild.dev/builds/ArtifactEquivalence@v0.1",
						ExternalParameters: ArtifactEquivalenceParams{
							Candidate: "rebuilt/package-1.0.0.tar.gz",
							Target:    "https://registry.npmjs.org/package/-/package-1.0.0.tgz",
						},
						InternalParameters: ServiceInternalParams{
							PrebuildSource: SourceLocation{
								Repository: "https://github.com/google/oss-rebuild",
								Ref:        "main",
							},
							ServiceSource: SourceLocation{
								Repository: "https://github.com/google/oss-rebuild",
								Ref:        "abcdef",
							},
						},
						ResolvedDependencies: ArtifactEquivalenceDeps{
							RebuiltArtifact: slsa1.ResourceDescriptor{
								Name:   "rebuild/package-1.0.0.tar.gz",
								Digest: common.DigestSet{"sha1": "def1234"},
							},
							UpstreamArtifact: slsa1.ResourceDescriptor{
								Name:   "https://registry.npmjs.org/package/-/package-1.0.0.tgz",
								Digest: common.DigestSet{"sha1": "abc123def456"},
							},
						},
					},
					RunDetails: ArtifactEquivalenceRunDetails{
						Builder: slsa1.Builder{
							ID: "https://github.com/google/oss-rebuild",
						},
						BuildMetadata: slsa1.BuildMetadata{
							InvocationID: "test-equivalence-123",
						},
						Byproducts: ArtifactEquivalenceByproducts{
							StabilizedArtifact: slsa1.ResourceDescriptor{
								Name:   "stabilized/package-1.0.0.tar.gz",
								Digest: common.DigestSet{"sha1": "f1234"},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.attestation)
			if err != nil {
				t.Fatalf("Marshal attestation failed: %v", err)
			}
			var restored ArtifactEquivalenceAttestation
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatalf("Unmarshal attestation failed: %v", err)
			}
			if diff := cmp.Diff(tt.attestation, restored); diff != "" {
				t.Errorf("Attestation JSON roundtrip mismatch (-want +got):\n%s", diff)
			}
			statement, err := tt.attestation.ToStatement()
			if err != nil {
				t.Fatalf("ToStatement failed: %v", err)
			}
			if statement == nil {
				t.Fatal("ToStatement returned nil")
			}
			slsaData, err := json.Marshal(statement)
			if err != nil {
				t.Fatalf("Marshal SLSA statement failed: %v", err)
			}
			var restoredSLSA in_toto.ProvenanceStatementSLSA1
			if err := json.Unmarshal(slsaData, &restoredSLSA); err != nil {
				t.Fatalf("Unmarshal SLSA statement failed: %v", err)
			}
			if diff := cmp.Diff(*statement, restoredSLSA); diff != "" {
				t.Errorf("SLSA statement JSON roundtrip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
