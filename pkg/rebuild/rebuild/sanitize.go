// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import "strings"

// Target Encoding
//
// Different representations of Target attributes have different constraints
// that may conflict with ecosystem package identifier conventions. This
// file provides bidirectional encoding/decoding to make package identifiers
// safe for storage while preserving the ability to reconstruct the canonical
// identifier.
//
// Ecosystem-specific package identifier constraints and encoding implementations
// are defined in their respective sanitize.go files.

// TargetTransform defines a bidirectional encoding/decoding transformation for a string field.
type TargetTransform struct {
	Encode func(string) string
	Decode func(string) string
}

// IdentityTransform is a no-op transform that returns the input unchanged.
var IdentityTransform = &TargetTransform{
	Encode: func(s string) string { return s },
	Decode: func(s string) string { return s },
}

// TargetEncoder defines how to encode each field of a Target for a specific encoding format.
type TargetEncoder struct {
	Package  *TargetTransform
	Version  *TargetTransform
	Artifact *TargetTransform
}

// targetEncoding represents an encoding format for package identifiers.
// Singleton instances are provided for each supported encoding format.
type targetEncoding struct {
	name string // for debugging/error messages
}

var (
	// FilesystemTargetEncoding encodes targets for filesystem paths and GCS object names.
	// Notable constraints:
	//
	// Filesystem (Windows):
	//   - Forbidden characters: \ / : * ? " < > |
	//   - Colon (:) is reserved for alternate file streams
	//   - Reference: https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file
	//
	// GCS Object Names:
	//   - Technically allows all Unicode, but same restrictions as Windows filesystem
	//     should be applied for cross-platform compatibility
	//   - Reference: https://cloud.google.com/storage/docs/objects
	FilesystemTargetEncoding = &targetEncoding{name: "filesystem"}

	// FirestoreTargetEncoding encodes targets for Firestore document IDs.
	//
	// Firestore Document IDs:
	//   - Forbidden: / (forward slash)
	//   - Cannot be: . or ..
	//   - Cannot match pattern: __.*__
	//   - Maximum: 1500 bytes UTF-8
	//   - Reference: https://firebase.google.com/docs/firestore/quotas
	FirestoreTargetEncoding = &targetEncoding{name: "firestore"}
)

// EncodedTarget represents a Target with fields encoded for a specific encoding format.
// Use targetEncoding.Encode() to create and EncodedTarget.Decode() to decode back to a canonical Target.
// For manual construction when parsing storage paths, use targetEncoding.New().
type EncodedTarget struct {
	Ecosystem Ecosystem
	Package   string
	Version   string
	Artifact  string
	encoding  *targetEncoding
}

// Encode encodes a Target using this encoding format, returning an EncodedTarget
// with encoded fields that are safe for use in that encoding's storage system.
//
// Example:
//
//	et := rebuild.FilesystemTargetEncoding.Encode(target)
//	path := filepath.Join(string(et.Ecosystem), et.Package, et.Version, et.Artifact)
func (te *targetEncoding) Encode(t Target) EncodedTarget {
	enc := getEncoder(t.Ecosystem, te)
	return EncodedTarget{
		Ecosystem: t.Ecosystem,
		Package:   enc.Package.Encode(t.Package),
		Version:   enc.Version.Encode(t.Version),
		Artifact:  enc.Artifact.Encode(t.Artifact),
		encoding:  te,
	}
}

// New creates an EncodedTarget for decoding purposes.
// Use this when manually constructing an EncodedTarget from parsed storage paths.
func (te *targetEncoding) New(eco Ecosystem, pkg, version, artifact string) EncodedTarget {
	return EncodedTarget{
		Ecosystem: eco,
		Package:   pkg,
		Version:   version,
		Artifact:  artifact,
		encoding:  te,
	}
}

// Decode decodes an EncodedTarget back to its canonical Target representation.
func (et EncodedTarget) Decode() Target {
	enc := getEncoder(et.Ecosystem, et.encoding)
	return Target{
		Ecosystem: et.Ecosystem,
		Package:   enc.Package.Decode(et.Package),
		Version:   enc.Version.Decode(et.Version),
		Artifact:  enc.Artifact.Decode(et.Artifact),
	}
}

// encoderKey uniquely identifies an encoder by ecosystem and target encoding.
type encoderKey struct {
	Ecosystem Ecosystem
	Encoding  *targetEncoding
}

// encoderRegistry maps (ecosystem, encoding) pairs to their encoders.
var encoderRegistry = make(map[encoderKey]*TargetEncoder)

// RegisterEncoder allows ecosystem packages to register encoding logic for a specific target encoding.
// This should be called from init() functions in ecosystem packages (e.g., pkg/rebuild/maven).
func RegisterEncoder(eco Ecosystem, te *targetEncoding, enc *TargetEncoder) {
	encoderRegistry[encoderKey{eco, te}] = enc
}

// defaultEncoder applies no transformation (identity transform for all fields).
var defaultEncoder = &TargetEncoder{
	Package:  IdentityTransform,
	Version:  IdentityTransform,
	Artifact: IdentityTransform,
}

// getEncoder retrieves the registered encoder or returns the default identity encoder.
func getEncoder(eco Ecosystem, te *targetEncoding) *TargetEncoder {
	if enc, ok := encoderRegistry[encoderKey{eco, te}]; ok {
		return enc
	}
	return defaultEncoder
}

// Transform Helpers

// MapTransform creates a Transform that maps characters according to the provided mapping.
// The reverse mapping for decoding is automatically computed.
func MapTransform(mappings map[rune]rune) *TargetTransform {
	// Build reverse mapping for decode
	reverse := make(map[rune]rune, len(mappings))
	for k, v := range mappings {
		reverse[v] = k
	}
	return &TargetTransform{
		Encode: func(s string) string {
			return strings.Map(func(r rune) rune {
				if mapped, ok := mappings[r]; ok {
					return mapped
				}
				return r
			}, s)
		},
		Decode: func(s string) string {
			return strings.Map(func(r rune) rune {
				if mapped, ok := reverse[r]; ok {
					return mapped
				}
				return r
			}, s)
		},
	}
}
