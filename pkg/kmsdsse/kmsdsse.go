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

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
)

type CloudKMSSignerVerifier struct {
	client *kms.KeyManagementClient
	key    *kmspb.CryptoKeyVersion
	pubpb  *kmspb.PublicKey
	pub    crypto.PublicKey
}

func NewCloudKMSSignerVerifier(ctx context.Context, c *kms.KeyManagementClient, k *kmspb.CryptoKeyVersion) (*CloudKMSSignerVerifier, error) {
	req := &kmspb.GetPublicKeyRequest{
		Name: k.Name,
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
	return &CloudKMSSignerVerifier{c, k, pubpb, pub}, nil
}

func (s *CloudKMSSignerVerifier) Public() crypto.PublicKey {
	return s.pub
}

func (s *CloudKMSSignerVerifier) Sign(ctx context.Context, data []byte) ([]byte, error) {
	// NOTE: We could pass Digest here instead to shrink the RPC size.
	req := &kmspb.AsymmetricSignRequest{
		Name: s.key.Name,
		Data: data,
	}
	resp, err := s.client.AsymmetricSign(ctx, req)
	if err != nil {
		return []byte{}, err
	}
	return resp.Signature, nil
}

func (s *CloudKMSSignerVerifier) Verify(ctx context.Context, data, sig []byte) error {
	switch s.pubpb.Algorithm {
	case kmspb.CryptoKeyVersion_EC_SIGN_P256_SHA256:
		h := sha256.New()
		ecKey, ok := s.pub.(*ecdsa.PublicKey)
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

func (s CloudKMSSignerVerifier) KeyID() (string, error) {
	return "https://cloudkms.googleapis.com/v1/" + s.key.Name, nil
}

var _ dsse.SignerVerifier = (*CloudKMSSignerVerifier)(nil)
