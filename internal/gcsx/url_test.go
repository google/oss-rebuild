// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcsx

import "testing"

func TestHTTPURL(t *testing.T) {
	got := HTTPURL("bucket", "path/to/obj")
	want := "https://storage.googleapis.com/bucket/path/to/obj"
	if got != want {
		t.Errorf("HTTPURL() = %q, want %q", got, want)
	}
}

func TestVirtualHostedURL(t *testing.T) {
	got := VirtualHostedURL("bucket", "prefix")
	want := "https://bucket.storage.googleapis.com/prefix"
	if got != want {
		t.Errorf("VirtualHostedURL() = %q, want %q", got, want)
	}
}

func TestMediaURL(t *testing.T) {
	got := MediaURL("bucket", "path/to obj", 123)
	want := "https://storage.googleapis.com/download/storage/v1/b/bucket/o/path%2Fto%20obj?alt=media&generation=123"
	if got != want {
		t.Errorf("MediaURL() = %q, want %q", got, want)
	}
}

func TestParseURL(t *testing.T) {
	for _, tc := range []struct {
		url            string
		bucket, object string
		wantErr        bool
	}{
		{url: "gs://bucket/path/to/obj", bucket: "bucket", object: "path/to/obj"},
		{url: "gs://bucket", bucket: "bucket"},
		{url: "gs://bucket/", bucket: "bucket"},
		{url: "https://bucket/obj", wantErr: true},
		{url: "bucket/obj", wantErr: true},
	} {
		bucket, object, err := ParseURL(tc.url)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseURL(%q) error = %v, wantErr %v", tc.url, err, tc.wantErr)
		}
		if bucket != tc.bucket || object != tc.object {
			t.Errorf("ParseURL(%q) = (%q, %q), want (%q, %q)", tc.url, bucket, object, tc.bucket, tc.object)
		}
	}
}
