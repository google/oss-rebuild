// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package urlx

import "net/url"

// MustParse will call url.Parse and panic if there is an error, returning on success.
func MustParse(rawURL string) *url.URL {
	if u, err := url.Parse(rawURL); err != nil {
		panic(err)
	} else {
		return u
	}
}
