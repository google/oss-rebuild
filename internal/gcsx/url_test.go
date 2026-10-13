// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcsx

import (
	"reflect"
	"testing"

	gcs "cloud.google.com/go/storage"
)

func TestBucketObject(t *testing.T) {
	got := Bucket("bucket").Object("path/to/obj")
	if want := (Ref{Bucket: "bucket", Object: "path/to/obj"}); got != want {
		t.Errorf("Bucket().Object() = %+v, want %+v", got, want)
	}
}

func TestParseURL(t *testing.T) {
	for _, tc := range []struct {
		url     string
		want    Ref
		wantErr bool
	}{
		{url: "gs://bucket/path/to/obj", want: Bucket("bucket").Object("path/to/obj")},
		{url: "gs://bucket", want: Bucket("bucket").Object("")},
		{url: "gs://bucket/", want: Bucket("bucket").Object("")},
		{url: "gs://bucket/obj#1234567890", want: Bucket("bucket").Object("obj").Generation(1234567890)},
		{url: "gs://bucket/obj#latest", wantErr: true},
		{url: "https://bucket/obj", wantErr: true},
		{url: "bucket/obj", wantErr: true},
	} {
		got, err := ParseURL(tc.url)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseURL(%q) error = %v, wantErr %v", tc.url, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("ParseURL(%q) = %+v, want %+v", tc.url, got, tc.want)
		}
	}
}

func TestMustParseURL(t *testing.T) {
	if got := MustParseURL("gs://bucket/obj"); got != Bucket("bucket").Object("obj") {
		t.Errorf("MustParseURL() = %+v", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("MustParseURL() did not panic on invalid input")
		}
	}()
	MustParseURL("not-a-url")
}

func TestHandle(t *testing.T) {
	client := &gcs.Client{}
	got := Bucket("bucket").Object("obj").Handle(client)
	if want := client.Bucket("bucket").Object("obj"); !reflect.DeepEqual(got, want) {
		t.Errorf("Handle() = %+v, want %+v", got, want)
	}
	got = Bucket("bucket").Object("obj").Generation(123).Handle(client)
	if want := client.Bucket("bucket").Object("obj").Generation(123); !reflect.DeepEqual(got, want) {
		t.Errorf("Handle() pinned = %+v, want %+v", got, want)
	}
}

func TestHTTPURL(t *testing.T) {
	got := Bucket("bucket").Object("path/to/obj").HTTPURL()
	want := "https://storage.googleapis.com/bucket/path/to/obj"
	if got != want {
		t.Errorf("HTTPURL() = %q, want %q", got, want)
	}
	got = Bucket("bucket").Object("obj").Generation(123).HTTPURL()
	want = "https://storage.googleapis.com/bucket/obj?generation=123"
	if got != want {
		t.Errorf("HTTPURL() pinned = %q, want %q", got, want)
	}
}

func TestVirtualHostedURL(t *testing.T) {
	got := Bucket("bucket").Object("prefix").VirtualHostedURL()
	want := "https://bucket.storage.googleapis.com/prefix"
	if got != want {
		t.Errorf("VirtualHostedURL() = %q, want %q", got, want)
	}
}

func TestMediaURL(t *testing.T) {
	got := Bucket("bucket").Object("path/to obj").Generation(123).MediaURL()
	want := "https://storage.googleapis.com/download/storage/v1/b/bucket/o/path%2Fto%20obj?alt=media&generation=123"
	if got != want {
		t.Errorf("MediaURL() = %q, want %q", got, want)
	}
}
