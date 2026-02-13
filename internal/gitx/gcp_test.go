// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gitx

import (
	"testing"
)

func TestIsSSMURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://us-central1-myproject.sourcemanager.dev/myproject/myrepo", true},
		{"https://europe-west1-proj.sourcemanager.dev/proj/repo", true},
		{"https://github.com/user/repo", false},
		{"https://gitlab.com/user/repo", false},
		{"file:///tmp/repo", false},
		{"", false},
		{"not a url ://", false},
		{"https://sourcemanager.dev/repo", false},
		{"https://evil-sourcemanager.dev.example.com/repo", false},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			if got := IsSSMURL(tc.url); got != tc.want {
				t.Errorf("IsSSMURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
