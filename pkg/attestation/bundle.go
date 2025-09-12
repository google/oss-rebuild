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

type VerifiedEnvelope[T any] struct {
	envelope *dsse.Envelope
	payload  *T
}

func (ve *VerifiedEnvelope[T]) Envelope() *dsse.Envelope {
	return ve.envelope
}

func NewVerifiedEnvelope[T any](ctx context.Context, e *dsse.Envelope, verifier *dsse.EnvelopeVerifier) (*VerifiedEnvelope[T], error) {
	if _, err := verifier.Verify(ctx, e); err != nil {
		return nil, errors.Wrap(err, "verifying envelope")
	}
	if e.PayloadType != in_toto.StatementInTotoV1 {
		return nil, errors.New("unexpected payload type")
	}
	if e.Payload == "" {
		return nil, errors.New("empty payload")
	}
	b, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return nil, errors.Wrap(err, "decoding base64 payload")
	}
	var decoded T
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil, errors.Wrap(err, "unmarshaling payload")
	}
	return &VerifiedEnvelope[T]{envelope: e, payload: &decoded}, nil
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
		ve, err := NewVerifiedEnvelope[in_toto.Statement](ctx, &env, verifier)
		if err != nil {
			return nil, errors.Wrap(err, "decoding payload")
		}
		envelopes = append(envelopes, *ve)
	}
	return &Bundle{envelopes: envelopes}, nil
}
