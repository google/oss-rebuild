// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package hashext

import (
	"crypto"
	"encoding/binary"
	"hash"
)

// MultiHash is an interface providing hash.Hash over multiple hash instances.
type MultiHash []TypedHash

// NewMultiHash creates a new MultiHash.
func NewMultiHash(hs ...crypto.Hash) MultiHash {
	var th MultiHash
	for _, algo := range hs {
		th = append(th, NewTypedHash(algo))
	}
	return th
}

// Sum writes all contained hashes.
func (m MultiHash) Write(p []byte) (int, error) {
	for _, th := range m {
		n, err := th.Write(p)
		if err != nil {
			return n, err
		}
	}
	return len(p), nil
}

// Sum constructs an aggregate by concatenating all hash Sum results with their algorithm types.
func (m MultiHash) Sum(b []byte) []byte {
	var result []byte
	for _, th := range m {
		result = binary.BigEndian.AppendUint64(result, uint64(th.Algorithm))
		result = append(result, th.Sum(b)...)
	}
	return result
}

// Reset calls Hash.Reset on all contained hashes.
func (m MultiHash) Reset() {
	for _, th := range m {
		th.Reset()
	}
}

// Size returns the size of the Sum.
func (m MultiHash) Size() int {
	var totalSize int
	for _, th := range m {
		// Format: uint64(algorithm) | hash.Sum()
		totalSize += 8
		totalSize += th.Size()
	}
	return totalSize
}

// BlockSize returns a representative block size or the smallest from contained hashes.
func (m MultiHash) BlockSize() int {
	size := m[0].BlockSize()
	for _, th := range m {
		if th.BlockSize() < size {
			size = th.BlockSize()
		}
	}
	return size
}

var _ hash.Hash = &MultiHash{}
