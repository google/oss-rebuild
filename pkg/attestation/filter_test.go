// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package attestation

import (
	"context"
	"strings"
	"testing"

	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
)

func TestReinterpretJSON(t *testing.T) {
	tests := []struct {
		name      string
		statement *in_toto.Statement
		expectErr bool
	}{
		{
			name: "valid statement to rebuild attestation",
			statement: &in_toto.Statement{
				StatementHeader: in_toto.StatementHeader{
					Type:          in_toto.StatementInTotoV1,
					PredicateType: slsa1.PredicateSLSAProvenance,
					Subject: []in_toto.Subject{{
						Name:   "test-package",
						Digest: common.DigestSet{"sha256": "abc123"},
					}},
				},
				Predicate: slsa1.ProvenancePredicate{
					BuildDefinition: slsa1.ProvenanceBuildDefinition{
						BuildType: BuildTypeRebuildV01,
						ExternalParameters: map[string]any{
							"artifact": "test-artifact",
						},
					},
				},
			},
		},
		{
			name: "statement with nil predicate",
			statement: &in_toto.Statement{
				StatementHeader: in_toto.StatementHeader{
					Type:          in_toto.StatementInTotoV1,
					PredicateType: "test-predicate",
					Subject: []in_toto.Subject{{
						Name:   "test-package",
						Digest: common.DigestSet{"sha256": "abc123"},
					}},
				},
				Predicate: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reinterpretJSON[RebuildAttestation](tt.statement)

			if tt.expectErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result == nil {
				t.Fatal("Result should not be nil")
			}

			// Verify the conversion preserved the statement header
			if result.Type != tt.statement.Type {
				t.Errorf("Type mismatch: want %s, got %s", tt.statement.Type, result.Type)
			}
			if result.PredicateType != tt.statement.PredicateType {
				t.Errorf("PredicateType mismatch: want %s, got %s", tt.statement.PredicateType, result.PredicateType)
			}
		})
	}
}

func TestFilterFor(t *testing.T) {
	ctx := context.Background()

	// Create test statements
	rebuildStatement := &in_toto.Statement{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			PredicateType: slsa1.PredicateSLSAProvenance,
			Subject: []in_toto.Subject{{
				Name:   "rebuild-package",
				Digest: common.DigestSet{"sha256": "rebuild123"},
			}},
		},
		Predicate: map[string]any{
			"buildDefinition": map[string]any{
				"buildType": BuildTypeRebuildV01,
			},
		},
	}

	equivalenceStatement := &in_toto.Statement{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			PredicateType: slsa1.PredicateSLSAProvenance,
			Subject: []in_toto.Subject{{
				Name:   "equivalence-package",
				Digest: common.DigestSet{"sha256": "equiv123"},
			}},
		},
		Predicate: map[string]any{
			"buildDefinition": map[string]any{
				"buildType": BuildTypeArtifactEquivalenceV01,
			},
		},
	}

	otherStatement := &in_toto.Statement{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			PredicateType: "https://example.com/other",
			Subject: []in_toto.Subject{{
				Name:   "other-package",
				Digest: common.DigestSet{"sha256": "other123"},
			}},
		},
		Predicate: map[string]any{
			"something": "else",
		},
	}

	// Create bundle with test envelopes
	envelope1 := createTestEnvelope(t, rebuildStatement)
	envelope2 := createTestEnvelope(t, equivalenceStatement)
	envelope3 := createTestEnvelope(t, otherStatement)

	envelopeVerifier := must(dsse.NewEnvelopeVerifier(&successVerifier{}))

	ve1 := must(NewVerifiedEnvelope[in_toto.Statement](ctx, envelope1, envelopeVerifier))
	ve2 := must(NewVerifiedEnvelope[in_toto.Statement](ctx, envelope2, envelopeVerifier))
	ve3 := must(NewVerifiedEnvelope[in_toto.Statement](ctx, envelope3, envelopeVerifier))

	bundle := &Bundle{
		envelopes: []VerifiedEnvelope[in_toto.Statement]{*ve1, *ve2, *ve3},
	}

	tests := []struct {
		name          string
		filters       []FilterOpt
		expectedCount int
	}{
		{
			name:          "no filters - return all",
			filters:       []FilterOpt{},
			expectedCount: 3,
		},
		{
			name:          "filter by SLSA predicate type",
			filters:       []FilterOpt{WithPredicateType(slsa1.PredicateSLSAProvenance)},
			expectedCount: 2,
		},
		{
			name:          "filter by other predicate type",
			filters:       []FilterOpt{WithPredicateType("https://example.com/other")},
			expectedCount: 1,
		},
		{
			name:          "filter by rebuild build type",
			filters:       []FilterOpt{WithBuildType(BuildTypeRebuildV01)},
			expectedCount: 1,
		},
		{
			name:          "filter by equivalence build type",
			filters:       []FilterOpt{WithBuildType(BuildTypeArtifactEquivalenceV01)},
			expectedCount: 1,
		},
		{
			name:          "filter by nonexistent build type",
			filters:       []FilterOpt{WithBuildType("nonexistent")},
			expectedCount: 0,
		},
		{
			name: "multiple filters - predicate type and build type",
			filters: []FilterOpt{
				WithPredicateType(slsa1.PredicateSLSAProvenance),
				WithBuildType(BuildTypeRebuildV01),
			},
			expectedCount: 1,
		},
		{
			name: "multiple filters - no matches",
			filters: []FilterOpt{
				WithPredicateType("https://example.com/other"),
				WithBuildType(BuildTypeRebuildV01),
			},
			expectedCount: 0,
		},
		{
			name: "generic filter - subjects containing 'rebuild'",
			filters: []FilterOpt{
				With(func(stmt *in_toto.Statement) bool {
					for _, subject := range stmt.Subject {
						if strings.Contains(subject.Name, "rebuild") {
							return true
						}
					}
					return false
				}),
			},
			expectedCount: 1,
		},
		{
			name: "generic filter - subjects with specific digest",
			filters: []FilterOpt{
				With(func(stmt *in_toto.Statement) bool {
					for _, subject := range stmt.Subject {
						if digest, ok := subject.Digest["sha256"]; ok && digest == "equiv123" {
							return true
						}
					}
					return false
				}),
			},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := FilterFor[in_toto.Statement](bundle, tt.filters...)
			if err != nil {
				t.Fatalf("FilterFor failed: %v", err)
			}

			if len(results) != tt.expectedCount {
				t.Errorf("Expected %d results, got %d", tt.expectedCount, len(results))
			}

			// Verify each result is properly converted
			for _, result := range results {
				if result == nil {
					t.Error("Result should not be nil")
				}
			}
		})
	}
}

func TestFilterForOne(t *testing.T) {
	ctx := context.Background()

	statement1 := &in_toto.Statement{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			PredicateType: slsa1.PredicateSLSAProvenance,
			Subject: []in_toto.Subject{{
				Name:   "unique-package",
				Digest: common.DigestSet{"sha256": "unique123"},
			}},
		},
		Predicate: slsa1.ProvenancePredicate{
			BuildDefinition: slsa1.ProvenanceBuildDefinition{
				BuildType: BuildTypeRebuildV01,
			},
		},
	}
	statement2 := &in_toto.Statement{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			PredicateType: slsa1.PredicateSLSAProvenance,
			Subject: []in_toto.Subject{{
				Name:   "another-package",
				Digest: common.DigestSet{"sha256": "another123"},
			}},
		},
		Predicate: slsa1.ProvenancePredicate{
			BuildDefinition: slsa1.ProvenanceBuildDefinition{
				BuildType: BuildTypeRebuildV01,
			},
		},
	}

	envelope1 := createTestEnvelope(t, statement1)
	envelope2 := createTestEnvelope(t, statement2)

	envelopeVerifier := must(dsse.NewEnvelopeVerifier(&successVerifier{}))

	ve1 := must(NewVerifiedEnvelope[in_toto.Statement](ctx, envelope1, envelopeVerifier))
	ve2 := must(NewVerifiedEnvelope[in_toto.Statement](ctx, envelope2, envelopeVerifier))

	tests := []struct {
		name         string
		bundle       *Bundle
		filters      []FilterOpt
		expectErrMsg string
	}{
		{
			name: "exactly one match",
			bundle: &Bundle{
				envelopes: []VerifiedEnvelope[in_toto.Statement]{*ve1},
			},
			filters: []FilterOpt{WithBuildType(BuildTypeRebuildV01)},
		},
		{
			name: "no matches",
			bundle: &Bundle{
				envelopes: []VerifiedEnvelope[in_toto.Statement]{*ve1},
			},
			filters:      []FilterOpt{WithBuildType("nonexistent")},
			expectErrMsg: "expected 1 result, got 0",
		},
		{
			name: "multiple matches",
			bundle: &Bundle{
				envelopes: []VerifiedEnvelope[in_toto.Statement]{*ve1, *ve2},
			},
			filters:      []FilterOpt{WithBuildType(BuildTypeRebuildV01)},
			expectErrMsg: "expected 1 result, got 2",
		},
		{
			name: "empty bundle",
			bundle: &Bundle{
				envelopes: []VerifiedEnvelope[in_toto.Statement]{},
			},
			filters:      []FilterOpt{},
			expectErrMsg: "expected 1 result, got 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := FilterForOne[in_toto.Statement](tt.bundle, tt.filters...)

			if tt.expectErrMsg != "" {
				if err == nil {
					t.Error("Expected error but got none")
					return
				}
				if !strings.Contains(err.Error(), tt.expectErrMsg) {
					t.Errorf("Error should contain %q, got: %v", tt.expectErrMsg, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result == nil {
				t.Fatal("Result should not be nil")
			}
		})
	}
}
