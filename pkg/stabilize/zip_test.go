// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package stabilize

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/archive"
	"github.com/google/oss-rebuild/pkg/archive/archivetest"
)

func TestStabilizeZip(t *testing.T) {
	testCases := []struct {
		test     string
		input    []*archive.ZipEntry
		expected []*archive.ZipEntry
	}{
		{
			test: "empty",
		},
		{
			test: "single",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo"}, Body: []byte("foo")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, Body: []byte("foo")},
			},
		},
		{
			test: "unordered",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo"}, Body: []byte("foo")},
				{FileHeader: &zip.FileHeader{Name: "bar"}, Body: []byte("bar")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "bar", Modified: time.UnixMilli(0)}, Body: []byte("bar")},
				{FileHeader: &zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, Body: []byte("foo")},
			},
		},
		{
			test: "strip-comment",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo", Comment: "bar"}, Body: []byte("foo")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, Body: []byte("foo")},
			},
		},
		{
			test: "strip-modified",
			input: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo", Modified: time.UnixMilli(1671890378000)}, Body: []byte("foo")},
			},
			expected: []*archive.ZipEntry{
				{FileHeader: &zip.FileHeader{Name: "foo", Modified: time.UnixMilli(0)}, Body: []byte("foo")},
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
			err := StabilizeZip(zr, zip.NewWriter(&output), NewContext(archive.ZipFormat).WithStabilizers(AllZipStabilizers))
			if err != nil {
				t.Fatalf("StabilizeZip(%v) = %v, want nil", tc.test, err)
			}
			var got []archive.ZipEntry
			{
				zr := must(zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len())))
				for _, ent := range zr.File {
					got = append(got, archive.ZipEntry{FileHeader: &ent.FileHeader, Body: must(io.ReadAll(must(ent.Open())))})
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

func TestStabilizeZipLocalTimeZone(t *testing.T) {
	input := must(archivetest.ZipFile([]archive.ZipEntry{
		{FileHeader: &zip.FileHeader{Name: "foo", Modified: time.UnixMilli(1671890378000)}, Body: []byte("foo")},
	}))
	stabilizeIn := func(loc *time.Location) []byte {
		orig := time.Local
		defer func() { time.Local = orig }()
		time.Local = loc
		var output bytes.Buffer
		zr := must(zip.NewReader(bytes.NewReader(input.Bytes()), int64(input.Len())))
		orDie(StabilizeZip(zr, zip.NewWriter(&output), NewContext(archive.ZipFormat).WithStabilizers(AllZipStabilizers)))
		return output.Bytes()
	}
	want := stabilizeIn(time.UTC)
	testCases := []struct {
		test string
		loc  *time.Location
	}{
		{test: "UTC", loc: time.UTC},
		{test: "EastOfUTC", loc: time.FixedZone("UTC+9", 9*60*60)},
		{test: "WestOfUTC", loc: time.FixedZone("UTC-5", -5*60*60)},
	}
	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			if diff := cmp.Diff(want, stabilizeIn(tc.loc)); diff != "" {
				t.Errorf("StabilizeZip() with local time zone %s mismatch (-UTC +got):\n%s", tc.loc, diff)
			}
		})
	}
}
