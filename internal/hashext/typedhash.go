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
