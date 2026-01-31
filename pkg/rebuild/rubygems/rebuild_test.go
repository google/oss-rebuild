// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rubygems

import (
	"testing"

	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestArtifactName(t *testing.T) {
	tests := []struct {
		target rebuild.Target
		want   string
	}{
		{
			target: rebuild.Target{Package: "rails", Version: "7.1.0"},
			want:   "rails-7.1.0.gem",
		},
		{
			target: rebuild.Target{Package: "activerecord", Version: "7.0.8"},
			want:   "activerecord-7.0.8.gem",
		},
		{
			target: rebuild.Target{Package: "nokogiri", Version: "1.15.4"},
			want:   "nokogiri-1.15.4.gem",
		},
	}

	for _, tt := range tests {
		got := ArtifactName(tt.target)
		if got != tt.want {
			t.Errorf("ArtifactName(%+v) = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestRebuilder_UsesTimewarp(t *testing.T) {
	r := Rebuilder{}
	input := rebuild.Input{
		Target: rebuild.Target{
			Ecosystem: rebuild.RubyGems,
			Package:   "rails",
			Version:   "7.1.0",
		},
	}
	if !r.UsesTimewarp(input) {
		t.Error("UsesTimewarp() = false, want true")
	}
}
