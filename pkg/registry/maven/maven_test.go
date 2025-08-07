// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"testing"

	"github.com/google/oss-rebuild/pkg/registry/maven"
)

func TestReleaseURL(t *testing.T) {
	r := HTTPRegistry{}
	url, err := r.ReleaseURL(context.Background(), "com.google.guava:guava", "33.4.8-jre", maven.TypePOM)
	if err != nil {
		t.Fatalf("ReleaseURL() error = %v", err)
	}
	expected := "https://repo1.maven.org/maven2/com/google/guava/guava/33.4.8-jre/guava-33.4.8-jre.pom"
	if url != expected {
		t.Errorf("ReleaseURL() = %v, want %v", url, expected)
	}
}
