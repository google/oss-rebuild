// Copyright 2024 The OSS Rebuild Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kmsdsse

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"

	kms "cloud.google.com/go/kms/apiv1"
	"github.com/pkg/errors"
	kmspb "google.golang.org/genproto/googleapis/cloud/kms/v1"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
)

type CloudKMSSigner struct {
	client *kms.KeyManagementClient
	key    *kmspb.CryptoKeyVersion
	pubpb  *kmspb.PublicKey
	pub    crypto.PublicKey
}

func NewCloudKMSSigner(ctx context.Context, c *kms.KeyManagementClient, k *kmspb.CryptoKeyVersion) (*CloudKMSSigner, error) {
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
	return &CloudKMSSigner{c, k, pubpb, pub}, nil
}

func (s *CloudKMSSigner) Public() crypto.PublicKey {
	return s.pub
}

func (s *CloudKMSSigner) Sign(ctx context.Context, data []byte) ([]byte, error) {
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

func (s *CloudKMSSigner) Verify(ctx context.Context, data, sig []byte) error {
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

func (s CloudKMSSigner) KeyID() (string, error) {
	return "https://cloudkms.googleapis.com/v1/" + s.key.Name, nil
}

var _ dsse.SignVerifier = (*CloudKMSSigner)(nil)
