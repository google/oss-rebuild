// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"strings"

	"github.com/google/oss-rebuild/internal/gcsx"
)

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
		ref, err := gcsx.ParseURL(originalURL)
		if err != nil {
			return "", err
		}
		return ref.HTTPURL(), nil
	}
	// For other URL schemes, return as-is
	return originalURL, nil
}
