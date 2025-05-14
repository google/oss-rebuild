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

type VerifiedEnvelope[T any] struct {
	envelope *dsse.Envelope
	payload  *T
}

func (ve *VerifiedEnvelope[T]) Envelope() *dsse.Envelope {
	return ve.envelope
}

type Bundle struct {
	envelopes []VerifiedEnvelope[in_toto.Statement]
}

func (b Bundle) Statements() []*in_toto.Statement {
	var s []*in_toto.Statement
	for _, env := range b.envelopes {
		s = append(s, env.payload)
	}
	return s
}

func decodeEnvelopePayload(e *dsse.Envelope) (*in_toto.Statement, error) {
	if e.Payload == "" {
		return nil, errors.New("empty payload")
	}
	b, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return nil, errors.Wrap(err, "decoding base64 payload")
	}
	var decoded in_toto.Statement
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil, errors.Wrap(err, "unmarshaling payload")
	}
	return &decoded, nil
}

func NewBundle(ctx context.Context, data []byte, verifier *dsse.EnvelopeVerifier) (*Bundle, error) {
	d := json.NewDecoder(bytes.NewBuffer(data))
	var envelopes []VerifiedEnvelope[in_toto.Statement]
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
		if env.PayloadType != in_toto.StatementInTotoV1 {
			return nil, errors.New("unexpected payload type")
		}
		payload, err := decodeEnvelopePayload(&env)
		if err != nil {
			return nil, errors.Wrap(err, "decoding payload")
		}
		envelopes = append(envelopes, VerifiedEnvelope[in_toto.Statement]{
			envelope: &env,
			payload:  payload,
		})
	}
	return &Bundle{envelopes: envelopes}, nil
}
