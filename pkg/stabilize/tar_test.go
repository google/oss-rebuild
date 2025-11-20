// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
)

func TestStabilizeTar(t *testing.T) {
	testCases := []struct {
		test     string
		input    []*archive.TarEntry
		expected []*archive.TarEntry
	}{
		{
			test: "empty",
		},
		{
			test: "single",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0644, ModTime: time.Now(), AccessTime: time.Now()}, Body: []byte("foo")},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("foo")},
			},
		},
		{
			test: "unordered",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0644}, Body: []byte("foo")},
				{Header: &tar.Header{Name: "bar", Typeflag: tar.TypeReg, Size: 3, Mode: 0644}, Body: []byte("bar")},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "bar", Typeflag: tar.TypeReg, Size: 3, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("bar")},
				{Header: &tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("foo")},
			},
		},
		{
			test: "strip-user-group",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Uid: 10, Uname: "user", Gid: 30, Gname: "group"}, Body: []byte("foo")},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, Body: []byte("foo")},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			// Construct tar from tc.input
			var input bytes.Buffer
			{
				zw := tar.NewWriter(&input)
				for _, entry := range tc.input {
					zw.WriteHeader(entry.Header)
					must(zw.Write(entry.Body))
				}
				zw.Close()
			}
			var output bytes.Buffer
			zr := tar.NewReader(bytes.NewReader(input.Bytes()))
			err := StabilizeTar(zr, tar.NewWriter(&output), StabilizeOpts{Stabilizers: AllTarStabilizers})
			if err != nil {
				t.Fatalf("StabilizeTar(%v) = %v, want nil", tc.test, err)
			}
			var got []*archive.TarEntry
			{
				zr := tar.NewReader(bytes.NewReader(output.Bytes()))
				for {
					th, err := zr.Next()
					if err == io.EOF {
						break
					}
					must(th, err)
					got = append(got, &archive.TarEntry{Header: th, Body: must(io.ReadAll(zr))})
				}
			}
			if len(got) != len(tc.expected) {
				t.Fatalf("StabilizeTar(%v) = %v, want %v", tc.test, got, tc.expected)
			}
			if !cmp.Equal(got, tc.expected) {
				t.Fatalf("StabilizeTar(%v) = %v, want %v\nDiff:\n%s", tc.test, got, tc.expected, cmp.Diff(got, tc.expected))
			}
		})
	}
}

func TestStabilizeCargoVCSHash(t *testing.T) {
	testCases := []struct {
		test        string
		input       []*archive.TarEntry
		expected    []*archive.TarEntry
		stabilizers []Stabilizer
	}{
		{
			test: "cargo_vcs_info.json with sha1",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: ".cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`{"git":{"sha1":"7e82b01cd4901f6a35b5153536f11b87f5e4e622"},"path_in_vcs":"aes-gcm"}`)},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: ".cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`{"git":{"sha1":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"path_in_vcs":"aes-gcm"}`)},
			},
			stabilizers: AllCrateStabilizers,
		},
		{
			test: "cargo_vcs_info.json in subdirectory",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "some-crate-1.0.0/.cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`{"git":{"sha1":"fa8197f11d79a079fcb1f6ef67fa9119ce6939b9"},"path_in_vcs":"some-crate"}`)},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "some-crate-1.0.0/.cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`{"git":{"sha1":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"path_in_vcs":"some-crate"}`)},
			},
			stabilizers: AllCrateStabilizers,
		},
		{
			test: "non-cargo_vcs_info.json file unchanged",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: "Cargo.toml", Typeflag: tar.TypeReg}, Body: []byte(`[package]
name = "test"
version = "1.0.0"`)},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: "Cargo.toml", Typeflag: tar.TypeReg}, Body: []byte(`[package]
name = "test"
version = "1.0.0"`)},
			},
			stabilizers: AllCrateStabilizers,
		},
		{
			test: "invalid JSON ignored",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: ".cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`invalid json`)},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: ".cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`invalid json`)},
			},
			stabilizers: AllCrateStabilizers,
		},
		{
			test: "malformed cargo_vcs_info.json without git field",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: ".cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`{"path_in_vcs":"some-path"}`)},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: ".cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`{"path_in_vcs":"some-path"}`)},
			},
			stabilizers: AllCrateStabilizers,
		},
		{
			test: "malformed cargo_vcs_info.json with git but no sha1",
			input: []*archive.TarEntry{
				{Header: &tar.Header{Name: ".cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`{"git":{"branch":"main"},"path_in_vcs":"test"}`)},
			},
			expected: []*archive.TarEntry{
				{Header: &tar.Header{Name: ".cargo_vcs_info.json", Typeflag: tar.TypeReg}, Body: []byte(`{"git":{"branch":"main"},"path_in_vcs":"test"}`)},
			},
			stabilizers: AllCrateStabilizers,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			// Construct tar from tc.input
			var input bytes.Buffer
			{
				zw := tar.NewWriter(&input)
				for _, entry := range tc.input {
					// Update size to match actual body length
					entry.Header.Size = int64(len(entry.Body))
					orDie(zw.WriteHeader(entry.Header))
					must(zw.Write(entry.Body))
				}
				zw.Close()
			}
			var output bytes.Buffer
			zr := tar.NewReader(bytes.NewReader(input.Bytes()))
			err := StabilizeTar(zr, tar.NewWriter(&output), StabilizeOpts{Stabilizers: tc.stabilizers})
			if err != nil {
				t.Fatalf("StabilizeTar(%v) = %v, want nil", tc.test, err)
			}
			var got []*archive.TarEntry
			{
				zr := tar.NewReader(bytes.NewReader(output.Bytes()))
				for {
					th, err := zr.Next()
					if err == io.EOF {
						break
					}
					must(th, err)
					got = append(got, &archive.TarEntry{Header: th, Body: must(io.ReadAll(zr))})
				}
			}
			if len(got) != len(tc.expected) {
				t.Fatalf("StabilizeTar(%v) = %d entries, want %d entries", tc.test, len(got), len(tc.expected))
			}
			for i, entry := range got {
				expected := tc.expected[i]
				if entry.Name != expected.Name {
					t.Errorf("Entry %d: Name = %q, want %q", i, entry.Name, expected.Name)
				}
				if !bytes.Equal(entry.Body, expected.Body) {
					t.Errorf("Entry %d (%s): Body = %q, want %q", i, entry.Name, string(entry.Body), string(expected.Body))
				}
				if entry.Size != int64(len(entry.Body)) {
					t.Errorf("Entry %d (%s): Size = %d, want %d (length of body)", i, entry.Name, entry.Size, len(entry.Body))
				}
			}
		})
	}
}
