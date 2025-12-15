// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// registerTestEncoder registers an encoder for testing and arranges for cleanup.
func registerTestEncoder(t *testing.T, eco Ecosystem, te *targetEncoding, enc *TargetEncoder) {
	t.Helper()
	key := encoderKey{eco, te}
	oldEnc, hadOld := encoderRegistry[key]
	RegisterEncoder(eco, te, enc)
	t.Cleanup(func() {
		if hadOld {
			encoderRegistry[key] = oldEnc
		} else {
			delete(encoderRegistry, key)
		}
	})
}

func TestFilesystemTargetEncoding(t *testing.T) {
	tests := []struct {
		name      string
		ecosystem Ecosystem
		encoder   *TargetEncoder
		canonical Target
		encoded   EncodedTarget
	}{
		{
			name:      "Maven package with colon",
			ecosystem: Maven,
			encoder: &TargetEncoder{
				Package:  MapTransform(map[rune]rune{':': '~'}),
				Version:  IdentityTransform,
				Artifact: IdentityTransform,
			},
			canonical: Target{
				Ecosystem: Maven,
				Package:   "org.apache.commons:commons-lang3",
				Version:   "3.12.0",
				Artifact:  "commons-lang3-3.12.0.jar",
			},
			encoded: EncodedTarget{
				Ecosystem: Maven,
				Package:   "org.apache.commons~commons-lang3",
				Version:   "3.12.0",
				Artifact:  "commons-lang3-3.12.0.jar",
				encoding:  FilesystemTargetEncoding,
			},
		},
		{
			name:      "Debian package with tilde in version",
			ecosystem: Debian,
			encoder: &TargetEncoder{
				Package:  MapTransform(map[rune]rune{'/': '~'}),
				Version:  IdentityTransform,
				Artifact: IdentityTransform,
			},
			canonical: Target{
				Ecosystem: Debian,
				Package:   "main/libfoo",
				Version:   "1.2.3~rc1-1",
				Artifact:  "libfoo_1.2.3~rc1-1_amd64.deb",
			},
			encoded: EncodedTarget{
				Ecosystem: Debian,
				Package:   "main~libfoo",
				Version:   "1.2.3~rc1-1",
				Artifact:  "libfoo_1.2.3~rc1-1_amd64.deb",
				encoding:  FilesystemTargetEncoding,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registerTestEncoder(t, tt.ecosystem, FilesystemTargetEncoding, tt.encoder)

			encoded := FilesystemTargetEncoding.Encode(tt.canonical)
			if diff := cmp.Diff(tt.encoded, encoded, cmp.AllowUnexported(EncodedTarget{}, targetEncoding{})); diff != "" {
				t.Errorf("FilesystemTargetEncoding.Encode() mismatch (-want +got):\n%s", diff)
			}

			decoded := encoded.Decode()
			if diff := cmp.Diff(tt.canonical, decoded); diff != "" {
				t.Errorf("EncodedTarget.Decode() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFirestoreTargetEncoding(t *testing.T) {
	registerTestEncoder(t, NPM, FirestoreTargetEncoding, &TargetEncoder{
		Package:  MapTransform(map[rune]rune{'/': '!'}),
		Version:  IdentityTransform,
		Artifact: IdentityTransform,
	})
	// NPM scoped packages contain '/' which must be encoded for Firestore
	canonical := Target{
		Ecosystem: NPM,
		Package:   "@fortawesome/react-fontawesome",
		Version:   "0.2.0",
		Artifact:  "react-fontawesome-0.2.0.tgz",
	}
	encoded := FirestoreTargetEncoding.Encode(canonical)
	want := EncodedTarget{
		Ecosystem: NPM,
		Package:   "@fortawesome!react-fontawesome",
		Version:   "0.2.0",
		Artifact:  "react-fontawesome-0.2.0.tgz",
		encoding:  FirestoreTargetEncoding,
	}
	if diff := cmp.Diff(want, encoded, cmp.AllowUnexported(EncodedTarget{}, targetEncoding{})); diff != "" {
		t.Errorf("FirestoreTargetEncoding.Encode() mismatch (-want +got):\n%s", diff)
	}
	decoded := encoded.Decode()
	if diff := cmp.Diff(canonical, decoded); diff != "" {
		t.Errorf("EncodedTarget.Decode() mismatch (-want +got):\n%s", diff)
	}
}

func TestDefaultEncoder(t *testing.T) {
	canonical := Target{
		Ecosystem: PyPI,
		Package:   "absl-py",
		Version:   "2.0.0",
		Artifact:  "absl_py-2.0.0-py3-none-any.whl",
	}
	encoded := FilesystemTargetEncoding.Encode(canonical)
	// Should be unchanged (identity transform)
	want := EncodedTarget{
		Ecosystem: PyPI,
		Package:   "absl-py",
		Version:   "2.0.0",
		Artifact:  "absl_py-2.0.0-py3-none-any.whl",
		encoding:  FilesystemTargetEncoding,
	}
	if diff := cmp.Diff(want, encoded, cmp.AllowUnexported(EncodedTarget{}, targetEncoding{})); diff != "" {
		t.Errorf("FilesystemTargetEncoding.Encode() mismatch (-want +got):\n%s", diff)
	}
	decoded := encoded.Decode()
	if diff := cmp.Diff(canonical, decoded); diff != "" {
		t.Errorf("EncodedTarget.Decode() mismatch (-want +got):\n%s", diff)
	}
}

func TestMapTransform(t *testing.T) {
	tests := []struct {
		name     string
		mappings map[rune]rune
		input    string
		want     string
	}{
		{
			name:     "multiple character mapping",
			mappings: map[rune]rune{':': '~', '/': '!', '*': '#'},
			input:    "org:apache/commons*test",
			want:     "org~apache!commons#test",
		},
		{
			name:     "single character replacement",
			mappings: map[rune]rune{':': '~'},
			input:    "org.apache.commons:commons-lang3",
			want:     "org.apache.commons~commons-lang3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transform := MapTransform(tt.mappings)
			encoded := transform.Encode(tt.input)
			if encoded != tt.want {
				t.Errorf("MapTransform.Encode() = %v, want %v", encoded, tt.want)
			}
			decoded := transform.Decode(encoded)
			if decoded != tt.input {
				t.Errorf("MapTransform.Decode() = %v, want %v", decoded, tt.input)
			}
		})
	}
}

func TestIdentityTransform(t *testing.T) {
	input := "test-package:with/chars"
	encoded := IdentityTransform.Encode(input)
	if encoded != input {
		t.Errorf("IdentityTransform.Encode() = %v, want %v (unchanged)", encoded, input)
	}
	decoded := IdentityTransform.Decode(encoded)
	if decoded != input {
		t.Errorf("IdentityTransform.Decode() = %v, want %v (unchanged)", decoded, input)
	}
}

func TestEncodedTargetNew(t *testing.T) {
	// Register Maven encoders for this test
	registerTestEncoder(t, Maven, FilesystemTargetEncoding, &TargetEncoder{
		Package:  MapTransform(map[rune]rune{':': '~'}),
		Version:  IdentityTransform,
		Artifact: IdentityTransform,
	})
	// Test manual construction for parsing scenarios
	et := FilesystemTargetEncoding.New(
		Maven,
		"org.apache.commons~commons-lang3",
		"3.12.0",
		"commons-lang3-3.12.0.jar",
	)
	want := EncodedTarget{
		Ecosystem: Maven,
		Package:   "org.apache.commons~commons-lang3",
		Version:   "3.12.0",
		Artifact:  "commons-lang3-3.12.0.jar",
		encoding:  FilesystemTargetEncoding,
	}
	if diff := cmp.Diff(want, et, cmp.AllowUnexported(EncodedTarget{}, targetEncoding{})); diff != "" {
		t.Errorf("FilesystemTargetEncoding.New() mismatch (-want +got):\n%s", diff)
	}
	decoded := et.Decode()
	wantDecoded := Target{
		Ecosystem: Maven,
		Package:   "org.apache.commons:commons-lang3",
		Version:   "3.12.0",
		Artifact:  "commons-lang3-3.12.0.jar",
	}
	if diff := cmp.Diff(wantDecoded, decoded); diff != "" {
		t.Errorf("EncodedTarget.Decode() mismatch (-want +got):\n%s", diff)
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name     string
		encoding *targetEncoding
		et       EncodedTarget
		errorMsg string
	}{
		{
			name:     "filesystem with forbidden slash in package",
			encoding: FilesystemTargetEncoding,
			et: EncodedTarget{
				Ecosystem: NPM,
				Package:   "@fortawesome/react-fontawesome",
				Version:   "0.2.0",
				Artifact:  "react-fontawesome-0.2.0.tgz",
			},
			errorMsg: "filesystem encoding violation: forbidden character '/' in Package field",
		},
		{
			name:     "filesystem with forbidden colon in package",
			encoding: FilesystemTargetEncoding,
			et: EncodedTarget{
				Ecosystem: Maven,
				Package:   "org.apache.commons:commons-lang3",
				Version:   "3.12.0",
				Artifact:  "commons-lang3-3.12.0.jar",
			},
			errorMsg: "filesystem encoding violation: forbidden character ':' in Package field",
		},
		{
			name:     "firestore with forbidden slash in package",
			encoding: FirestoreTargetEncoding,
			et: EncodedTarget{
				Ecosystem: NPM,
				Package:   "@fortawesome/react-fontawesome",
				Version:   "0.2.0",
				Artifact:  "react-fontawesome-0.2.0.tgz",
			},
			errorMsg: "firestore encoding violation: forbidden character '/' in Package field",
		},
		{
			name:     "filesystem with forbidden character in version",
			encoding: FilesystemTargetEncoding,
			et: EncodedTarget{
				Ecosystem: PyPI,
				Package:   "test-pkg",
				Version:   "1.0.0/beta",
				Artifact:  "test_pkg-1.0.0.whl",
			},
			errorMsg: "filesystem encoding violation: forbidden character '/' in Version field",
		},
		{
			name:     "filesystem with forbidden character in artifact",
			encoding: FilesystemTargetEncoding,
			et: EncodedTarget{
				Ecosystem: PyPI,
				Package:   "test-pkg",
				Version:   "1.0.0",
				Artifact:  "test:pkg-1.0.0.whl",
			},
			errorMsg: "filesystem encoding violation: forbidden character ':' in Artifact field",
		},
		{
			name:     "filesystem with multiple forbidden characters",
			encoding: FilesystemTargetEncoding,
			et: EncodedTarget{
				Ecosystem: Maven,
				Package:   "org:apache/commons",
				Version:   "3.12.0",
				Artifact:  "commons-lang3-3.12.0.jar",
			},
			errorMsg: "filesystem encoding violation: forbidden character ':' in Package field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.encoding.Validate(tt.et)
			if err == nil {
				t.Errorf("Validate() expected error but got nil")
			} else if !strings.Contains(err.Error(), tt.errorMsg) {
				t.Errorf("Validate() error = %q, want to contain %q", err.Error(), tt.errorMsg)
			}
		})
	}
}

func TestEncodeWithValidateFailure(t *testing.T) {
	// Register a broken encoder that doesn't transform the problematic character
	registerTestEncoder(t, NPM, FilesystemTargetEncoding, &TargetEncoder{
		Package:  IdentityTransform, // This should transform '/' but doesn't
		Version:  IdentityTransform,
		Artifact: IdentityTransform,
	})
	target := Target{
		Ecosystem: NPM,
		Package:   "@fortawesome/react-fontawesome",
		Version:   "0.2.0",
		Artifact:  "react-fontawesome-0.2.0.tgz",
	}
	// Should panic because the encoded output still contains '/'
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Encode() with broken encoder should have panicked")
		}
	}()
	FilesystemTargetEncoding.Encode(target)
}
