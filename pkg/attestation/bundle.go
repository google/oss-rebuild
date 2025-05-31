// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package attestation provides utilities for working with OSS Rebuild attestations.
package attestation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"

	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
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

type VerifiedEnvelope struct {
	Raw     *dsse.Envelope
	Payload *in_toto.ProvenanceStatementSLSA1
}

type Bundle struct {
	envelopes []VerifiedEnvelope
}

func decodeEnvelopePayload(e *dsse.Envelope) (*in_toto.ProvenanceStatementSLSA1, error) {
	if e.Payload == "" {
		return nil, errors.New("empty payload")
	}
	b, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return nil, errors.Wrap(err, "decoding base64 payload")
	}
	var decoded in_toto.ProvenanceStatementSLSA1
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil, errors.Wrap(err, "unmarshaling payload")
	}
	return &decoded, nil
}

func NewBundle(ctx context.Context, data []byte, verifier *dsse.EnvelopeVerifier) (*Bundle, error) {
	d := json.NewDecoder(bytes.NewBuffer(data))
	var envelopes []VerifiedEnvelope
	for {
		var env dsse.Envelope
		if err := d.Decode(&env); err != nil {
			if err == io.EOF {
				break
			}
			return nil, errors.Wrap(err, "decoding envelope")
		}
		if _, err := verifier.Verify(ctx, &env); err != nil {
			return nil, errors.Wrap(err, "verifying envelope")
		}
		payload, err := decodeEnvelopePayload(&env)
		if err != nil {
			return nil, errors.Wrap(err, "decoding payload")
		}
		envelopes = append(envelopes, VerifiedEnvelope{
			Raw:     &env,
			Payload: payload,
		})
	}
	return &Bundle{envelopes: envelopes}, nil
}

func (b *Bundle) Payloads() []*in_toto.ProvenanceStatementSLSA1 {
	result := make([]*in_toto.ProvenanceStatementSLSA1, len(b.envelopes))
	for i, env := range b.envelopes {
		result[i] = env.Payload
	}
	return result
}

func (b *Bundle) RebuildAttestation() (*in_toto.ProvenanceStatementSLSA1, error) {
	for _, env := range b.envelopes {
		if env.Payload.Predicate.BuildDefinition.BuildType == BuildTypeRebuildV01 {
			return env.Payload, nil
		}
	}
	return nil, errors.New("no rebuild attestation found")
}

func (b *Bundle) Byproduct(name string) ([]byte, error) {
	att, err := b.RebuildAttestation()
	if err != nil {
		return nil, errors.Wrap(err, "getting rebuild attestation")
	}
	for _, b := range att.Predicate.RunDetails.Byproducts {
		if b.Name == name {
			return b.Content, nil
		}
	}
	return nil, errors.Errorf("byproduct %q not found", name)
}
