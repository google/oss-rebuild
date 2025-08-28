// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package maven

import (
	"context"
	"testing"
)

func TestReleaseURL(t *testing.T) {
	testCases := []struct {
		test     string
		pkg      string
		version  string
		artifact string
		want     string
	}{
		{
			test:     "guava_pom",
			pkg:      "com.google.guava:guava",
			version:  "33.4.8-jre",
			artifact: "guava-33.4.8-jre.pom",
			want:     "https://repo1.maven.org/maven2/com/google/guava/guava/33.4.8-jre/guava-33.4.8-jre.pom",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.test, func(t *testing.T) {
			r := HTTPRegistry{}
			url, err := r.ReleaseURL(context.Background(), tc.pkg, tc.version, tc.artifact)
			if err != nil {
				t.Fatalf("ReleaseURL() error = %v", err)
			}
			if url != tc.want {
				t.Errorf("ReleaseURL() = %v, want %v", url, tc.want)
			}
		})
	}
}
