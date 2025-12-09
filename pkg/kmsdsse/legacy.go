// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package kmsdsse

import (
	"context"
	"crypto"

	"github.com/secure-systems-lab/go-securesystemslib/dsse"
)

const legacyKMSURLPrefix = "https://cloudkms.googleapis.com/v1/"

// LegacyKeyIDVerifier wraps a CloudKMSSignerVerifier and returns a legacy HTTPS URL
// format keyid for backward compatibility with existing attestations.
type LegacyKeyIDVerifier struct {
	inner *CloudKMSSignerVerifier
}

// NewLegacyKeyIDVerifier creates a verifier that uses the legacy HTTPS URL keyid format.
func NewLegacyKeyIDVerifier(inner *CloudKMSSignerVerifier) dsse.Verifier {
	return &LegacyKeyIDVerifier{inner: inner}
}

// KeyID returns the legacy HTTPS URL format of the key identifier.
func (v *LegacyKeyIDVerifier) KeyID() (string, error) {
	return legacyKMSURLPrefix + v.inner.keyName, nil
}

// Verify delegates to the wrapped verifier.
func (v *LegacyKeyIDVerifier) Verify(ctx context.Context, data, sig []byte) error {
	return v.inner.Verify(ctx, data, sig)
}

// Public delegates to the wrapped verifier.
func (v *LegacyKeyIDVerifier) Public() crypto.PublicKey {
	return v.inner.Public()
}
