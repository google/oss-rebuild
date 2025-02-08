// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var epoch = time.UnixMilli(0)

func TestStabilizeTar(t *testing.T) {
	testCases := []struct {
		test     string
		input    []*TarEntry
		expected []*TarEntry
	}{
		{
			test: "empty",
		},
		{
			test: "single",
			input: []*TarEntry{
				{&tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0644, ModTime: time.Now(), AccessTime: time.Now()}, []byte("foo")},
			},
			expected: []*TarEntry{
				{&tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("foo")},
			},
		},
		{
			test: "unordered",
			input: []*TarEntry{
				{&tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0644}, []byte("foo")},
				{&tar.Header{Name: "bar", Typeflag: tar.TypeReg, Size: 3, Mode: 0644}, []byte("bar")},
			},
			expected: []*TarEntry{
				{&tar.Header{Name: "bar", Typeflag: tar.TypeReg, Size: 3, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("bar")},
				{&tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("foo")},
			},
		},
		{
			test: "strip-user-group",
			input: []*TarEntry{
				{&tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Uid: 10, Uname: "user", Gid: 30, Gname: "group"}, []byte("foo")},
			},
			expected: []*TarEntry{
				{&tar.Header{Name: "foo", Typeflag: tar.TypeReg, Size: 3, Mode: 0777, ModTime: epoch, AccessTime: epoch, PAXRecords: map[string]string{"atime": "0"}, Format: tar.FormatPAX}, []byte("foo")},
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
			var got []*TarEntry
			{
				zr := tar.NewReader(bytes.NewReader(output.Bytes()))
				for {
					th, err := zr.Next()
					if err == io.EOF {
						break
					}
					must(th, err)
					got = append(got, &TarEntry{th, must(io.ReadAll(zr))})
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
