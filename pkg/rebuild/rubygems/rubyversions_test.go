// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRubyVersionForRubygems(t *testing.T) {
	type result struct {
		Ruby  string
		Exact bool
	}
	tests := []struct {
		name            string
		rubygemsVersion string
		want            result
		wantErr         bool
	}{
		{
			name:            "exact match - Ruby 3.4.0 bundles 3.6.2",
			rubygemsVersion: "3.6.2",
			want:            result{Ruby: "3.4.0", Exact: true},
		},
		{
			name:            "exact match - Ruby 3.3.0 bundles 3.5.3",
			rubygemsVersion: "3.5.3",
			want:            result{Ruby: "3.3.0", Exact: true},
		},
		{
			name:            "exact match - Ruby 3.3.6 bundles 3.5.22",
			rubygemsVersion: "3.5.22",
			want:            result{Ruby: "3.3.6", Exact: true},
		},
		{
			name:            "exact match - Ruby 3.1.3 bundles 3.3.26",
			rubygemsVersion: "3.3.26",
			want:            result{Ruby: "3.1.3", Exact: true},
		},
		{
			name:            "exact match - Ruby 3.1.1 bundles 3.3.7",
			rubygemsVersion: "3.3.7",
			want:            result{Ruby: "3.1.1", Exact: true},
		},
		{
			name:            "non-bundled version falls back to series",
			rubygemsVersion: "3.5.23",
			want:            result{Ruby: "3.3.11", Exact: false},
		},
		{
			name:            "non-bundled version in 3.4 series",
			rubygemsVersion: "3.4.15",
			want:            result{Ruby: "3.2.11", Exact: false},
		},
		{
			name:            "dev version stripped and falls back",
			rubygemsVersion: "3.6.0.dev",
			want:            result{Ruby: "3.4.9", Exact: false},
		},
		{
			name:            "pre version stripped",
			rubygemsVersion: "3.5.0.pre1",
			want:            result{Ruby: "3.3.11", Exact: false},
		},
		{
			name:            "unknown series returns error",
			rubygemsVersion: "9.9.9",
			wantErr:         true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ruby, exact, err := rubyVersionForRubygems(tc.rubygemsVersion)
			if (err != nil) != tc.wantErr {
				t.Fatalf("rubyVersionForRubygems(%q) error = %v, wantErr %v", tc.rubygemsVersion, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			got := result{Ruby: ruby, Exact: exact}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("rubyVersionForRubygems(%q) returned diff (-want +got):\n%s", tc.rubygemsVersion, diff)
			}
		})
	}
}

func TestRubygemsVersionForRuby(t *testing.T) {
	tests := []struct {
		name        string
		rubyVersion string
		want        string
	}{
		{"Ruby 3.4.0", "3.4.0", "3.6.2"},
		{"Ruby 3.3.0", "3.3.0", "3.5.3"},
		{"Ruby 3.3.6", "3.3.6", "3.5.22"},
		{"Ruby 3.1.0", "3.1.0", "3.3.3"},
		{"Ruby 2.7.0", "2.7.0", "3.1.2"},
		{"unknown version", "99.99.99", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rubygemsVersionForRuby(tc.rubyVersion)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("rubygemsVersionForRuby(%q) returned diff (-want +got):\n%s", tc.rubyVersion, diff)
			}
		})
	}
}

func TestCleanRubygemsVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no suffix", "3.6.2", "3.6.2"},
		{"dev suffix", "3.6.0.dev", "3.6.0"},
		{"pre suffix", "3.5.0.pre1", "3.5.0"},
		{"regular patch", "3.5.22", "3.5.22"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanRubygemsVersion(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("cleanRubygemsVersion(%q) returned diff (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}

func TestMajorMinor(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"three parts", "3.5.22", "3.5"},
		{"three parts zero patch", "3.4.0", "3.4"},
		{"two series", "2.7.8", "2.7"},
		{"single part", "3", "3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := majorMinor(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("majorMinor(%q) returned diff (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}
