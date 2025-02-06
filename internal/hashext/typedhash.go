// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package hashext provides extensions to the standard crypto/hash package.
package hashext

import (
	"crypto"
	"hash"
)

// TypedHash is a hash.Hash annotated with its algorithm.
type TypedHash struct {
	hash.Hash
	Algorithm crypto.Hash
}

// NewTypedHash constructs a new TypedHash.
func NewTypedHash(algo crypto.Hash) TypedHash {
	return TypedHash{Hash: algo.New(), Algorithm: algo}
}
