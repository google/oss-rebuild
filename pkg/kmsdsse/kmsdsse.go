// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package kmsdsse

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"regexp"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
)

type CloudKMSSignerVerifier struct {
	client  *kms.KeyManagementClient
	keyName string
	pubpb   *kmspb.PublicKey
	pub     crypto.PublicKey
}

// keyNameRegex is a compiled regular expression to validate the format of a Cloud KMS CryptoKeyVersion resource name.
var keyNameRegex = regexp.MustCompile(
	`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+/cryptoKeyVersions/[^/]+$`,
)

func NewCloudKMSSignerVerifier(ctx context.Context, c *kms.KeyManagementClient, keyName string) (*CloudKMSSignerVerifier, error) {
	if !keyNameRegex.MatchString(keyName) {
		return nil, errors.Errorf("invalid key name format: %q; expected format: %s", keyName, keyNameRegex.String())
	}
	req := &kmspb.GetPublicKeyRequest{
		Name: keyName,
	}
	pubpb, err := c.GetPublicKey(ctx, req)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode([]byte(pubpb.Pem))
	if blk == nil || blk.Bytes == nil {
		return nil, errors.New("failed to decode PEM public key")
	}
	pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse PEM public key")
	}
	return &CloudKMSSignerVerifier{
		client:  c,
		keyName: keyName,
		pubpb:   pubpb,
		pub:     pub,
	}, nil
}

func (s *CloudKMSSignerVerifier) Public() crypto.PublicKey {
	return s.pub
}

func (s *CloudKMSSignerVerifier) Sign(ctx context.Context, data []byte) ([]byte, error) {
	var digest kmspb.Digest
	switch s.pubpb.Algorithm {
	case kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256:
		digestBytes := sha256.Sum256(data)
		digest.Digest = &kmspb.Digest_Sha256{Sha256: digestBytes[:]}
	default:
		return nil, errors.New("unsupported key type")
	}
	req := &kmspb.AsymmetricSignRequest{
		Name:   s.keyName,
		Digest: &digest,
	}
	resp, err := s.client.AsymmetricSign(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Signature, nil
}

func (s *CloudKMSSignerVerifier) Verify(ctx context.Context, data, sig []byte) error {
	switch s.pubpb.Algorithm {
	case kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256:
		ecKey, ok := s.pub.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("unexpected public key type")
		}
		sum := sha256.Sum256(data)
		if !ecdsa.VerifyASN1(ecKey, sum[:], sig) {
			return errors.New("signature verification failed")
		}
		return nil
	// TODO: Support more key types as necessary.
	default:
		return errors.New("unsupported key type")
	}
}

func (s CloudKMSSignerVerifier) KeyID() (string, error) {
	return "https://cloudkms.googleapis.com/v1/" + s.keyName, nil
}

var _ dsse.SignerVerifier = (*CloudKMSSignerVerifier)(nil)
