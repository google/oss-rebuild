// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
	"time"
)

func TestStabilizeZip(t *testing.T) {
	testCases := []struct {
		test     string
		input    []*ZipEntry
		expected []*ZipEntry
	}{
		{
			test: "empty",
		},
		{
			test: "single",
			input: []*ZipEntry{
				{&zip.FileHeader{Name: "foo"}, []byte("foo")},
			},
			expected: []*ZipEntry{
				{&zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, []byte("foo")},
			},
		},
		{
			test: "unordered",
			input: []*ZipEntry{
				{&zip.FileHeader{Name: "foo"}, []byte("foo")},
				{&zip.FileHeader{Name: "bar"}, []byte("bar")},
			},
			expected: []*ZipEntry{
				{&zip.FileHeader{Name: "bar", Modified: time.UnixMilli(0)}, []byte("bar")},
				{&zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, []byte("foo")},
			},
		},
		{
			test: "strip-comment",
			input: []*ZipEntry{
				{&zip.FileHeader{Name: "foo", Comment: "bar"}, []byte("foo")},
			},
			expected: []*ZipEntry{
				{&zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, []byte("foo")},
			},
		},
		{
			test: "strip-modified",
			input: []*ZipEntry{
				{&zip.FileHeader{Name: "foo", Modified: time.UnixMilli(1671890378000)}, []byte("foo")},
			},
			expected: []*ZipEntry{
				{&zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, []byte("foo")},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			// Construct zip from tc.input
			var input bytes.Buffer
			{
				zw := zip.NewWriter(&input)
				for _, entry := range tc.input {
					orDie(entry.WriteTo(zw))
				}
				orDie(zw.Close())
			}
			var output bytes.Buffer
			zr := must(zip.NewReader(bytes.NewReader(input.Bytes()), int64(input.Len())))
			err := StabilizeZip(zr, zip.NewWriter(&output), StabilizeOpts{Stabilizers: AllZipStabilizers})
			if err != nil {
				t.Fatalf("StabilizeZip(%v) = %v, want nil", tc.test, err)
			}
			var got []ZipEntry
			{
				zr := must(zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len())))
				for _, ent := range zr.File {
					got = append(got, ZipEntry{&ent.FileHeader, must(io.ReadAll(must(ent.Open())))})
				}
			}
			if len(got) != len(tc.expected) {
				t.Fatalf("StabilizeZip(%v) = %v, want %v", tc.test, got, tc.expected)
			}
			for i := range got {
				if !all(
					got[i].FileHeader.Name == tc.expected[i].FileHeader.Name,
					bytes.Equal(got[i].Body, tc.expected[i].Body),
					got[i].FileHeader.Modified.Equal(tc.expected[i].FileHeader.Modified),
					got[i].FileHeader.Comment == tc.expected[i].FileHeader.Comment,
				) {
					t.Fatalf("StabilizeZip(%v) = %v, want %v", tc.test, got, tc.expected)
				}
			}
		})
	}
}

func must[T any](t T, err error) T {
	orDie(err)
	return t
}

func orDie(err error) {
	if err != nil {
		panic(err)
	}
}

func all(predicates ...bool) bool {
	for _, v := range predicates {
		if !v {
			return false
		}
	}
	return true
}

func TestToZipCompatibleReader(t *testing.T) {
	tests := []struct {
		name       string
		input      io.Reader
		size       int64
		expectRead bool
	}{
		{
			name:  "Test with Seekable ReaderAt",
			input: bytes.NewReader([]byte("test data")),
			size:  9,
		},
		{
			name:       "Test with Non-Seekable ReaderAt",
			input:      &noSeekReaderAt{bytes.NewReader([]byte("test data")), false},
			size:       9,
			expectRead: true,
		},
		{
			name:       "Test with non-ReadAt Reader",
			input:      &noReadAtSeeker{bytes.NewReader([]byte("test data")), false},
			size:       9,
			expectRead: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			readerAt, size, err := toZipCompatibleReader(tc.input)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if readerAt == nil {
				t.Errorf("Unexpected nil reader")
			}
			if size != tc.size {
				t.Errorf("Expected size %d but got %d", tc.size, size)
			}
			if tc.expectRead && !tc.input.(readSpy).ReadCalled() {
				t.Error("Expected reader to have been read")
			}
		})
	}
}

type readSpy interface {
	io.Reader
	ReadCalled() bool
}

type noSeekReaderAt struct {
	io.ReaderAt
	readCalled bool
}

func (ns *noSeekReaderAt) ReadCalled() bool { return ns.readCalled }

func (ns *noSeekReaderAt) Read(p []byte) (n int, err error) {
	ns.readCalled = true
	return ns.ReaderAt.(io.Reader).Read(p)
}

func (ns *noSeekReaderAt) ReadAt(p []byte, off int64) (int, error) { return ns.ReaderAt.ReadAt(p, off) }

type noReadAtSeeker struct {
	io.ReadSeeker
	readCalled bool
}

func (ns *noReadAtSeeker) ReadCalled() bool { return ns.readCalled }

func (ns *noReadAtSeeker) Read(p []byte) (n int, err error) {
	ns.readCalled = true
	return ns.ReadSeeker.Read(p)
}

func (ns *noReadAtSeeker) Seek(off int64, w int) (int64, error) { return ns.ReadSeeker.Seek(off, w) }
