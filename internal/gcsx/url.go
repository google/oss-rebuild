// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcsx

import (
	"net/url"
	"strconv"
	"strings"

	gcs "cloud.google.com/go/storage"
	"github.com/pkg/errors"
)

// Bucket identifies a GCS bucket.
type Bucket string

// Object returns a ref for the named object in the bucket.
func (b Bucket) Object(name string) Ref {
	return Ref{Bucket: string(b), Object: name}
}

// Ref identifies a GCS object, optionally pinned to a generation.
type Ref struct {
	Bucket string
	Object string
	gen    int64
}

// ParseURL parses a gs:// URL into a Ref. A gsutil-style #<generation>
// suffix pins the ref to that generation.
func ParseURL(gsURL string) (Ref, error) {
	rest, ok := strings.CutPrefix(gsURL, "gs://")
	if !ok {
		return Ref{}, errors.Errorf("not a gs:// URL: %s", gsURL)
	}
	var r Ref
	path, gen, pinned := strings.Cut(rest, "#")
	if pinned {
		var err error
		r.gen, err = strconv.ParseInt(gen, 10, 64)
		if err != nil {
			return Ref{}, errors.Wrapf(err, "parsing generation %q", gen)
		}
	}
	r.Bucket, r.Object, _ = strings.Cut(path, "/")
	return r, nil
}

// MustParseURL is ParseURL, panicking on error.
func MustParseURL(gsURL string) Ref {
	r, err := ParseURL(gsURL)
	if err != nil {
		panic(err)
	}
	return r
}

// Generation returns the ref pinned to the given generation.
func (r Ref) Generation(gen int64) Ref {
	r.gen = gen
	return r
}

// Handle returns the object's handle on the given client, pinned to the
// ref's generation when set.
func (r Ref) Handle(client *gcs.Client) *gcs.ObjectHandle {
	obj := client.Bucket(r.Bucket).Object(r.Object)
	if r.gen != 0 {
		obj = obj.Generation(r.gen)
	}
	return obj
}

// query renders the given parameters plus the generation pin, if any.
func (r Ref) query(q url.Values) string {
	if r.gen != 0 {
		q.Set("generation", strconv.FormatInt(r.gen, 10))
	}
	return q.Encode()
}

// HTTPURL returns the object's path-style download endpoint.
func (r Ref) HTTPURL() string {
	u := url.URL{
		Scheme:   "https",
		Host:     "storage.googleapis.com",
		Path:     "/" + r.Bucket + "/" + r.Object,
		RawQuery: r.query(url.Values{}),
	}
	return u.String()
}

// VirtualHostedURL returns the object's XML API endpoint with the bucket as
// the host subdomain.
func (r Ref) VirtualHostedURL() string {
	u := url.URL{
		Scheme:   "https",
		Host:     r.Bucket + ".storage.googleapis.com",
		Path:     "/" + r.Object,
		RawQuery: r.query(url.Values{}),
	}
	return u.String()
}

// MediaURL returns the object's JSON API media download endpoint.
func (r Ref) MediaURL() string {
	u := url.URL{
		Scheme: "https",
		Host:   "storage.googleapis.com",
		// The object is a single path segment in the JSON API: RawPath
		// carries its escaped form and Path the decoded form, matching the
		// GCS client's httpStorageClient.newRangeReaderXML. Escaping rules:
		// https://cloud.google.com/storage/docs/request-endpoints#encoding
		Path:     "/download/storage/v1/b/" + r.Bucket + "/o/" + r.Object,
		RawPath:  "/download/storage/v1/b/" + url.PathEscape(r.Bucket) + "/o/" + url.PathEscape(r.Object),
		RawQuery: r.query(url.Values{"alt": []string{"media"}}),
	}
	return u.String()
}
