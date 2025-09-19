// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package attestation

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	slsa1 "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v1"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
)

// successVerifier implements dsse.Verifier for testing
type successVerifier struct{}

func (v *successVerifier) Verify(ctx context.Context, data, sig []byte) error { return nil }
func (v *successVerifier) KeyID() (string, error)                             { return "test-key", nil }
func (v *successVerifier) Public() crypto.PublicKey                           { return nil }

// failingVerifier implements dsse.Verifier that always fails verification
type failingVerifier struct {
	err error
}

func (v *failingVerifier) Verify(ctx context.Context, data, sig []byte) error { return v.err }
func (v *failingVerifier) KeyID() (string, error)                             { return "failing-key", nil }
func (v *failingVerifier) Public() crypto.PublicKey                           { return nil }

func createTestEnvelope(t *testing.T, statement *in_toto.Statement) *dsse.Envelope {
	t.Helper()
	payload := must(json.Marshal(statement))
	return &dsse.Envelope{
		PayloadType: InTotoPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []dsse.Signature{{Sig: base64.StdEncoding.EncodeToString([]byte("test-signature"))}},
	}
}

func TestVerifiedEnvelope_NewVerifiedEnvelope(t *testing.T) {
	ctx := context.Background()

	testStatement := &in_toto.Statement{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			PredicateType: slsa1.PredicateSLSAProvenance,
			Subject: []in_toto.Subject{{
				Name:   "test-package",
				Digest: common.DigestSet{"sha256": "abc123def456"},
			}},
		},
		Predicate: map[string]any{
			"buildType": BuildTypeRebuildV01,
		},
	}
	validEnvelope := createTestEnvelope(t, testStatement)

	tests := []struct {
		name         string
		envelope     *dsse.Envelope
		verifier     dsse.Verifier
		expectErrMsg string
	}{
		{
			name:     "valid envelope",
			envelope: validEnvelope,
			verifier: &successVerifier{},
		},
		{
			name:         "verification fails",
			envelope:     validEnvelope,
			verifier:     &failingVerifier{err: errors.New("verification failed")},
			expectErrMsg: "verifying envelope",
		},
		{
			name: "wrong payload type",
			envelope: &dsse.Envelope{
				PayloadType: "wrong-type",
				Payload:     validEnvelope.Payload,
				Signatures:  validEnvelope.Signatures,
			},
			verifier:     &successVerifier{},
			expectErrMsg: "unexpected payload type",
		},
		{
			name: "empty payload",
			envelope: &dsse.Envelope{
				PayloadType: InTotoPayloadType,
				Payload:     "",
				Signatures:  validEnvelope.Signatures,
			},
			verifier:     &successVerifier{},
			expectErrMsg: "empty payload",
		},
		{
			name: "invalid base64",
			envelope: &dsse.Envelope{
				PayloadType: InTotoPayloadType,
				Payload:     "invalid-base64-!!!",
				Signatures:  validEnvelope.Signatures,
			},
			verifier:     &successVerifier{},
			expectErrMsg: "unable to base64 decode payload",
		},
		{
			name: "invalid json",
			envelope: &dsse.Envelope{
				PayloadType: InTotoPayloadType,
				Payload:     base64.StdEncoding.EncodeToString([]byte("invalid json")),
				Signatures:  validEnvelope.Signatures,
			},
			verifier:     &successVerifier{},
			expectErrMsg: "unmarshaling payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelopeVerifier := must(dsse.NewEnvelopeVerifier(tt.verifier))
			result, err := NewVerifiedEnvelope[in_toto.Statement](ctx, tt.envelope, envelopeVerifier)
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

			if result.envelope != tt.envelope {
				t.Error("Envelope should be preserved")
			}

			if result.payload == nil {
				t.Error("Payload should not be nil")
			}

			// Verify the payload was correctly decoded
			if diff := cmp.Diff(testStatement, result.payload); diff != "" {
				t.Errorf("Payload mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBundle_NewBundle(t *testing.T) {
	ctx := context.Background()

	// Create test statements and envelopes
	statement1 := &in_toto.Statement{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			PredicateType: slsa1.PredicateSLSAProvenance,
			Subject: []in_toto.Subject{{
				Name:   "package-1",
				Digest: common.DigestSet{"sha256": "package1hash"},
			}},
		},
		Predicate: map[string]any{
			"buildType": BuildTypeRebuildV01,
		},
	}

	statement2 := &in_toto.Statement{
		StatementHeader: in_toto.StatementHeader{
			Type:          in_toto.StatementInTotoV1,
			PredicateType: "https://slsa.dev/attestation/v1",
			Subject: []in_toto.Subject{{
				Name:   "package-2",
				Digest: common.DigestSet{"sha256": "package2hash"},
			}},
		},
		Predicate: map[string]any{
			"buildType": BuildTypeArtifactEquivalenceV01,
		},
	}

	envelope1 := createTestEnvelope(t, statement1)
	envelope2 := createTestEnvelope(t, statement2)

	// Create bundle data (newline-delimited JSON)
	validBundleData := func() []byte {
		var buf bytes.Buffer
		orDie(json.NewEncoder(&buf).Encode(envelope1))
		orDie(json.NewEncoder(&buf).Encode(envelope2))
		return buf.Bytes()
	}()

	tests := []struct {
		name          string
		data          []byte
		verifier      dsse.Verifier
		expectErrMsg  string
		expectedCount int
	}{
		{
			name:          "valid bundle",
			data:          validBundleData,
			verifier:      &successVerifier{},
			expectedCount: 2,
		},
		{
			name:          "empty data",
			data:          []byte(""),
			verifier:      &successVerifier{},
			expectedCount: 0,
		},
		{
			name:         "invalid json",
			data:         []byte("invalid json"),
			verifier:     &successVerifier{},
			expectErrMsg: "decoding envelope",
		},
		{
			name:         "verification fails",
			data:         validBundleData,
			verifier:     &failingVerifier{err: errors.New("verification failed")},
			expectErrMsg: "decoding payload",
		},
		{
			name: "mixed valid and invalid envelopes",
			data: func() []byte {
				var buf bytes.Buffer
				orDie(json.NewEncoder(&buf).Encode(envelope1))
				buf.WriteString("invalid json\n")
				return buf.Bytes()
			}(),
			verifier:     &successVerifier{},
			expectErrMsg: "decoding envelope",
		},
		{
			name: "envelope with wrong payload type",
			data: func() []byte {
				wrongEnvelope := *envelope1
				wrongEnvelope.PayloadType = "wrong-type"
				return must(json.Marshal(wrongEnvelope))
			}(),
			verifier:     &successVerifier{},
			expectErrMsg: "unexpected payload type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelopeVerifier := must(dsse.NewEnvelopeVerifier(tt.verifier))

			bundle, err := NewBundle(ctx, tt.data, envelopeVerifier)

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
			if bundle == nil {
				t.Fatal("Bundle should not be nil")
			}
			if len(bundle.envelopes) != tt.expectedCount {
				t.Errorf("Expected %d envelopes, got %d", tt.expectedCount, len(bundle.envelopes))
			}
			// Verify statements can be retrieved
			statements := bundle.Statements()
			if len(statements) != tt.expectedCount {
				t.Errorf("Expected %d statements, got %d", tt.expectedCount, len(statements))
			}
		})
	}
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func orDie(err error) {
	if err != nil {
		panic(err)
	}
}
