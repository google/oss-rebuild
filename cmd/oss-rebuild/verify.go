// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"strings"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/google/oss-rebuild/pkg/kmsdsse"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
)

const kmsV1API = "https://cloudkms.googleapis.com/v1/"
const gcpKMSScheme = "gcpkms://"
const ossRebuildKeyResource = "projects/oss-rebuild/locations/global/keyRings/ring/cryptoKeys/signing-key/cryptoKeyVersions/1"
const ossRebuildKeyURI = gcpKMSScheme + ossRebuildKeyResource

type key struct {
	crypto.PublicKey
	ID        string
	Algorithm kmspb.CryptoKeyVersion_CryptoKeyVersionAlgorithm
}

var ossRebuildKey = key{
	PublicKey: mustParsePKIX(`-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEXkyL5IFxz/Hg6DwUy0HBumXcMxt9
nQSECAK6r262hPwIzjd6LpE7IPlUbwgheE87vU8EUE9tsS02MShFZGo1gg==
-----END PUBLIC KEY-----
`),
	ID:        ossRebuildKeyURI,
	Algorithm: kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256,
}

var embeddedKeys []key = []key{ossRebuildKey}

func mustParsePKIX(pubkey string) crypto.PublicKey {
	key, err := parsePKIX(pubkey)
	if err != nil {
		panic(err)
	}
	return key
}

func parsePKIX(pubkey string) (crypto.PublicKey, error) {
	blk, _ := pem.Decode([]byte(pubkey))
	if blk == nil || blk.Bytes == nil {
		return nil, errors.New("failed to decode PEM public key")
	}
	pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse PEM public key")
	}
	return pub, nil
}

func makeKMSVerifier(ctx context.Context, cryptoKeyVersion string) (dsse.Verifier, error) {
	// Handle both old HTTPS format and new gcpkms:// format
	if strings.HasPrefix(cryptoKeyVersion, kmsV1API) {
		cryptoKeyVersion = strings.TrimPrefix(cryptoKeyVersion, kmsV1API)
	} else if strings.HasPrefix(cryptoKeyVersion, gcpKMSScheme) {
		cryptoKeyVersion = strings.TrimPrefix(cryptoKeyVersion, gcpKMSScheme)
	}
	kc, err := kms.NewKeyManagementClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating KMS client")
	}
	kmsVerifier, err := kmsdsse.NewCloudKMSSignerVerifier(ctx, kc, cryptoKeyVersion)
	if err != nil {
		return nil, errors.Wrap(err, "creating Cloud KMS verifier")
	}
	return kmsVerifier, nil
}

type keyVerifier struct {
	key key
}

func (s *keyVerifier) Public() crypto.PublicKey {
	return s.key.PublicKey
}

func (s *keyVerifier) Verify(ctx context.Context, data, sig []byte) error {
	switch s.key.Algorithm {
	case kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256:
		h := sha256.New()
		ecKey, ok := s.key.PublicKey.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("unexpected public key type")
		}
		h.Write(data)
		if !ecdsa.VerifyASN1(ecKey, h.Sum(nil), sig) {
			return errors.New("signature verification failed")
		}
		return nil
	// TODO: Support more key types as necessary.
	default:
		return errors.New("unsupported key type")
	}
}

func (s keyVerifier) KeyID() (string, error) {
	return s.key.ID, nil
}

var _ dsse.Verifier = (*keyVerifier)(nil)

type trustAllVerifier struct{}

func (v *trustAllVerifier) Verify(ctx context.Context, data, sig []byte) error { return nil }
func (v *trustAllVerifier) KeyID() (string, error)                             { return "", nil }
func (v *trustAllVerifier) Public() crypto.PublicKey                           { return nil }
