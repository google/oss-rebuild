// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package verifier

import (
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"

	"github.com/google/oss-rebuild/internal/hashext"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/in-toto/in-toto-golang/in_toto"
	"github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/common"
	"github.com/pkg/errors"
	"github.com/secure-systems-lab/go-securesystemslib/dsse"
)

// InTotoEnvelopeSigner is a wrapper around dsse.EnvelopeSigner that implements a higher-level signing operation for In Toto resources.
type InTotoEnvelopeSigner struct {
	*dsse.EnvelopeSigner
}

// SignStatement produces a DSSE Envelope for the provided ProvenanceStatement.
func (signer *InTotoEnvelopeSigner) SignStatement(ctx context.Context, s *in_toto.ProvenanceStatementSLSA1) (*dsse.Envelope, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, errors.Wrap(err, "marshalling statement")
	}
	envelope, err := signer.SignPayload(ctx, s.StatementHeader.Type, b)
	if err != nil {
		return nil, errors.Wrap(err, "signing payload")
	}
	return envelope, nil
}

// toNISTName converts a crypto.Hash to its corresponding NIST name.
// See https://github.com/in-toto/attestation/blob/main/spec/v1/digest_set.md#supported-algorithms
func toNISTName(h crypto.Hash) string {
	switch h {
	case crypto.SHA256:
		return "sha256"
	case crypto.SHA224:
		return "sha224"
	case crypto.SHA384:
		return "sha384"
	case crypto.SHA512:
		return "sha512"
	case crypto.SHA512_224:
		return "sha512_224"
	case crypto.SHA512_256:
		return "sha512_256"
	case crypto.SHA3_224:
		return "sha3_224"
	case crypto.SHA3_256:
		return "sha3_256"
	case crypto.SHA3_384:
		return "sha3_384"
	case crypto.SHA3_512:
		return "sha3_512"
	case crypto.BLAKE2b_256, crypto.BLAKE2b_384, crypto.BLAKE2b_512:
		return "blake2b"
	case crypto.BLAKE2s_256:
		return "blake2s"
	case crypto.RIPEMD160:
		return "ripemd160"
	case crypto.SHA1:
		return "sha1"
	case crypto.MD5:
		return "md5"
	// NOTE: No separate constants for the "shake128" and "shake256" sha3-based hashes.
	default:
		panic("unsupported hash algorithm")
	}
}

// makeDigestSet converts a set of TypedHash objects to a DigestSet.
func makeDigestSet(hs ...hashext.TypedHash) common.DigestSet {
	ret := make(common.DigestSet, len(hs))
	for _, h := range hs {
		ret[toNISTName(h.Algorithm)] = hex.EncodeToString(h.Sum(nil))
	}
	return ret
}

// gitDigestSet returns a DigestSet corresponding to the provided Location's git commit hash.
func gitDigestSet(loc rebuild.Location) common.DigestSet {
	for _, h := range []crypto.Hash{crypto.SHA1, crypto.SHA256} {
		// Compare hex len to bytes len.
		if len(loc.Ref) == 2*h.Size() {
			return common.DigestSet{toNISTName(h): loc.Ref}
		}
	}
	panic("unsupported git ref")
}
