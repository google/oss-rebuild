// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package archive

import (
	"bytes"
	"io"
	"testing"
)

func must[T any](t T, err error) T {
	orDie(err)
	return t
}

func orDie(err error) {
	if err != nil {
		panic(err)
	}
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
			readerAt, size, err := ToZipCompatibleReader(tc.input)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if readerAt == nil {
				t.Errorf("Unexpected nil reader")
			}
			if size != tc.size {
				t.Errorf("size = %d, want %d", size, tc.size)
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
