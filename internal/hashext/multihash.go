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
