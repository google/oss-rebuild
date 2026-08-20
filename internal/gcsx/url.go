// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcsx

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// ParseURL splits a gs:// URL into its bucket and object path.
func ParseURL(gsURL string) (bucket, object string, err error) {
	rest, ok := strings.CutPrefix(gsURL, "gs://")
	if !ok {
		return "", "", errors.Errorf("not a gs:// URL: %s", gsURL)
	}
	bucket, object, _ = strings.Cut(rest, "/")
	return bucket, object, nil
}

// HTTPURL returns the object's path-style download endpoint.
func HTTPURL(bucket, object string) string {
	u := url.URL{Scheme: "https", Host: "storage.googleapis.com", Path: "/" + bucket + "/" + object}
	return u.String()
}

// VirtualHostedURL returns the object's XML API endpoint with the bucket as
// the host subdomain.
func VirtualHostedURL(bucket, object string) string {
	u := url.URL{Scheme: "https", Host: bucket + ".storage.googleapis.com", Path: "/" + object}
	return u.String()
}

// MediaURL returns the object's JSON API media download endpoint, pinned to
// generation when nonzero.
func MediaURL(bucket, object string, generation int64) string {
	q := url.Values{"alt": []string{"media"}}
	if generation != 0 {
		q.Set("generation", strconv.FormatInt(generation, 10))
	}
	u := url.URL{
		Scheme: "https",
		Host:   "storage.googleapis.com",
		// The object is a single path segment in the JSON API: its slashes
		// must be escaped.
		Path:     "/download/storage/v1/b/" + bucket + "/o/" + object,
		RawPath:  "/download/storage/v1/b/" + url.PathEscape(bucket) + "/o/" + url.PathEscape(object),
		RawQuery: q.Encode(),
	}
	return u.String()
}
