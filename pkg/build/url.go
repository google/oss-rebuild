// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"fmt"
	"net/url"
	"strings"
)

// gsToHTTP converts a gs:// URL to an HTTP URL
func gsToHTTP(gsURL string) (string, error) {
	if !strings.HasPrefix(gsURL, "gs://") {
		return "", fmt.Errorf("not a gs:// URL: %s", gsURL)
	}
	u, err := url.Parse(gsURL)
	if err != nil {
		return "", fmt.Errorf("invalid gs:// URL: %w", err)
	}
	bucket := u.Host
	object := strings.TrimPrefix(u.Path, "/")
	httpURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucket, object)
	return httpURL, nil
}

// NeedsAuth determines if a URL requires authentication based on configured prefixes
func NeedsAuth(url string, authPrefixes []string) bool {
	for _, prefix := range authPrefixes {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// ConvertURLForRuntime converts a storage URL to a runtime-appropriate URL
// For example, converts gs:// URLs to HTTP URLs
func ConvertURLForRuntime(originalURL string) (string, error) {
	if strings.HasPrefix(originalURL, "gs://") {
		return gsToHTTP(originalURL)
	}
	// For other URL schemes, return as-is
	return originalURL, nil
}
