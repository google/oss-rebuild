// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Package pbdigest provides utilities for working with protobuf digests.
package pbdigest

import (
	"crypto"
	_ "crypto/sha256" // Register SHA256 with the crypto package.
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
)

var (
	// hexStringRegex doesn't contain the size because that's checked separately.
	hexStringRegex = regexp.MustCompile("^[a-f0-9]+$")

	// HashFn is the digest function used.
	HashFn crypto.Hash = crypto.SHA256

	// Empty is the digest of the empty blob.
	Empty = NewFromBlob([]byte{})

	// copyBufs is a pool of 32KiB []byte slices, used to compute hashes.
	copyBufs = sync.Pool{
		New: func() any {
			buf := make([]byte, 32*1024)
			return &buf
		},
	}
)

// Digest is a Go type to represent a digest of a message.
// Ported from https://github.com/bazelbuild/remote-apis-sdks
type Digest struct {
	Hash string
	Size int64
}

// New creates a new digest from a string and size. It does some basic
// validation, which makes it marginally superior to constructing a Digest
// yourself. It returns an empty digest and an error if the hash/size are invalid.
func New(hash string, size int64) (Digest, error) {
	d := Digest{Hash: hash, Size: size}
	if err := d.Validate(); err != nil {
		return Empty, err
	}
	return d, nil
}

// NewFromFile computes a file digest from a path.
// It returns an error if there was a problem accessing the file.
func NewFromFile(path string) (Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Empty, err
	}
	defer f.Close()
	return NewFromReader(f)
}

// NewFromReader computes a file digest from a reader.
// It returns an error if there was a problem reading the file.
func NewFromReader(r io.Reader) (Digest, error) {
	h := HashFn.New()
	buf := copyBufs.Get().(*[]byte)
	defer copyBufs.Put(buf)
	size, err := io.CopyBuffer(h, r, *buf)
	if err != nil {
		return Empty, err
	}
	return Digest{
		Hash: hex.EncodeToString(h.Sum(nil)),
		Size: size,
	}, nil
}

// String returns a hash in a canonical form of hash/size.
func (d Digest) String() string {
	return fmt.Sprintf("%s/%d", d.Hash, d.Size)
}

// NewFromMessage calculates the digest of a protobuf in SHA-256 mode.
// It returns an error if the proto marshalling failed.
func NewFromMessage(msg proto.Message) (Digest, error) {
	blob, err := proto.Marshal(msg)
	if err != nil {
		return Empty, err
	}
	return NewFromBlob(blob), nil
}

// NewFromBlob takes a blob (in the form of a byte array) and returns the
// Digest for that blob. Changing this function will lead to cache
// invalidations (execution cache and potentially others).
// This cannot return an error, since the result is valid by definition.
func NewFromBlob(blob []byte) Digest {
	h := HashFn.New()
	h.Write(blob)
	arr := h.Sum(nil)
	return Digest{Hash: hex.EncodeToString(arr[:]), Size: int64(len(blob))}
}

// NewFromString returns a digest from a canonical digest string.
// It returns an error if the hash/size are invalid.
func NewFromString(s string) (Digest, error) {
	pair := strings.Split(s, "/")
	if len(pair) != 2 {
		return Empty, fmt.Errorf("expected digest in the form hash/size, got %s", s)
	}
	size, err := strconv.ParseInt(pair[1], 10, 64)
	if err != nil {
		return Empty, fmt.Errorf("invalid size in digest %s: %s", s, err)
	}
	return New(pair[0], size)
}

// Validate returns nil if a digest appears to be valid, or a descriptive error
// if it is not. All functions accepting digests directly from clients should
// call this function, whether it's via an RPC call or by reading a serialized
// proto message that contains digests that was uploaded directly from the
// client.
func (d Digest) Validate() error {
	length := len(d.Hash)
	if length != HashFn.Size()*2 {
		return fmt.Errorf("valid hash length is %d, got length %d (%s)", HashFn.Size()*2, length, d.Hash)
	}
	if !hexStringRegex.MatchString(d.Hash) {
		return fmt.Errorf("hash is not a lowercase hex string (%s)", d.Hash)
	}
	if d.Size < 0 {
		return fmt.Errorf("expected non-negative size, got %d", d.Size)
	}
	return nil
}
