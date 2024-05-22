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
	"bytes"
	"crypto"
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/binary"
	"testing"
)

func TestMultiHash(t *testing.T) {
	// Create TypedHash instances directly
	sha256Hash := NewTypedHash(crypto.SHA256)
	sha512Hash := NewTypedHash(crypto.SHA512)

	// Construct the MultiHash using TypedHash
	hashes := MultiHash{sha256Hash, sha512Hash}

	// Test Write
	data := []byte("test data")
	n, err := hashes.Write(data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write returned %d, expected %d", n, len(data))
	}

	// Test Sum
	expectedSum := append(
		binary.BigEndian.AppendUint64(nil, uint64(sha256Hash.Algorithm)),
		sha256Hash.Sum(nil)...,
	)
	expectedSum = append(
		binary.BigEndian.AppendUint64(expectedSum, uint64(sha512Hash.Algorithm)),
		sha512Hash.Sum(nil)...,
	)
	if !bytes.Equal(hashes.Sum(nil), expectedSum) {
		t.Errorf("Incorrect Sum result")
	}

	// Test Reset
	hashes.Reset()
	if !bytes.Equal(sha256Hash.Sum(nil), crypto.SHA256.New().Sum(nil)) {
		t.Errorf("Reset did not clear SHA256 hash")
	}
	if !bytes.Equal(sha512Hash.Sum(nil), crypto.SHA512.New().Sum(nil)) {
		t.Errorf("Reset did not clear SHA512 hash")
	}

	// Test Size
	if hashes.Size() != 8+sha256Hash.Size()+8+sha512Hash.Size() {
		t.Errorf("Incorrect Size calculation")
	}

	// Test BlockSize
	if hashes.BlockSize() != min(sha256Hash.BlockSize(), sha512Hash.BlockSize()) {
		t.Errorf("Incorrect BlockSize calculation")
	}
}

func TestMultiHashSingleHash(t *testing.T) {
	// Create a MultiHash with only SHA256
	hashes := MultiHash{NewTypedHash(crypto.SHA256)}

	// Test Write
	data := []byte("test data")
	n, err := hashes.Write(data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write returned %d, expected %d", n, len(data))
	}

	// Test Sum
	expectedSum := append(
		binary.BigEndian.AppendUint64(nil, uint64(crypto.SHA256)),
		hashes[0].Sum(nil)...,
	)
	if !bytes.Equal(hashes.Sum(nil), expectedSum) {
		t.Errorf("Incorrect Sum result")
	}

	// Test Reset
	hashes.Reset()
	if !bytes.Equal(hashes[0].Sum(nil), crypto.SHA256.New().Sum(nil)) {
		t.Errorf("Reset did not clear SHA256 hash")
	}

	// Test Size
	if hashes.Size() != 8+hashes[0].Size() {
		t.Errorf("Incorrect Size calculation")
	}

	// Test BlockSize
	if hashes.BlockSize() != hashes[0].BlockSize() {
		t.Errorf("Incorrect BlockSize calculation")
	}
}
